// Package tools - cron job management
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// CronJob cron job
type CronJob struct {
	ID          string    `json:"id"`
	Schedule    string    `json:"schedule"`
	Command     string    `json:"command"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	Status      string    `json:"status"` // "active" | "deleted"
	LastFire    time.Time `json:"last_fire,omitempty"`
}

// CronFireFunc is the callback function type when a cron job fires
type CronFireFunc func(job *CronJob)

// CronManager cron job manager with scheduling support
type CronManager struct {
	mu       sync.RWMutex
	jobs     map[string]*CronJob
	nextID   atomic.Int64
	onFire   CronFireFunc
	stopCh   chan struct{}
	running  bool
	stopOnce sync.Once
	fireSem  chan struct{}
}

// NewCronManager creates cron job manager with scheduler
func NewCronManager() *CronManager {
	return &CronManager{
		jobs:    make(map[string]*CronJob),
		stopCh:  make(chan struct{}),
		fireSem: make(chan struct{}, 10),
	}
}

// SetOnFire sets the callback function for when a cron job fires
func (cm *CronManager) SetOnFire(fn CronFireFunc) {
	cm.onFire = fn
}

// Start starts the cron scheduler
func (cm *CronManager) Start() {
	cm.mu.Lock()
	if cm.running {
		cm.mu.Unlock()
		return
	}
	cm.running = true
	cm.mu.Unlock()

	go cm.schedulerLoop()
	log.Println("[CronManager] scheduler started")
}

// Stop stops the cron scheduler
func (cm *CronManager) Stop() {
	cm.stopOnce.Do(func() {
		cm.mu.Lock()
		defer cm.mu.Unlock()
		if !cm.running {
			return
		}
		close(cm.stopCh)
		cm.running = false
		log.Println("[CronManager] scheduler stopped")
	})
}

// schedulerLoop is the main scheduling loop that checks every second.
// Also performs periodic cleanup of deleted jobs every 5 minutes.
func (cm *CronManager) schedulerLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Track last cleanup time for periodic deleted job cleanup
	lastCleanup := time.Now()
	const cleanupInterval = 5 * time.Minute

	for {
		select {
		case <-cm.stopCh:
			return
		case now := <-ticker.C:
			cm.checkAndFire(now)
			// Periodic cleanup of deleted jobs to prevent memory leak
			if now.Sub(lastCleanup) >= cleanupInterval {
				if count := cm.CleanDeleted(); count > 0 {
					log.Printf("[CronManager] periodic cleanup: removed %d deleted jobs", count)
				}
				lastCleanup = now
			}
		}
	}
}

// checkAndFire checks all active cron jobs and fires those that match the current time
func (cm *CronManager) checkAndFire(now time.Time) {
	cm.mu.RLock()
	var activeJobs []*CronJob
	for _, job := range cm.jobs {
		if job.Status == "active" {
			activeJobs = append(activeJobs, job)
		}
	}
	cm.mu.RUnlock()

	for _, job := range activeJobs {
		if cm.shouldFire(job.Schedule, now) {
			cm.mu.Lock()
			job.LastFire = now
			cm.mu.Unlock()

			if cm.onFire != nil {
				cm.fireSem <- struct{}{}
				go func(j *CronJob) {
					defer func() { <-cm.fireSem }()
					cm.onFire(j)
				}(job)
			}
		}
	}
}

// shouldFire checks if a cron schedule matches the given time
// 兼容 5/6/7 字段表达式，支持 L/W/LW/# 修饰符（M08-03 改造，）
func (cm *CronManager) shouldFire(schedule string, now time.Time) bool {
	sched, err := ParseCronSchedule(schedule)
	if err != nil {
		return false
	}

	second := now.Second()
	minute := now.Minute()
	hour := now.Hour()
	day := now.Day()
	month := int(now.Month())
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	year := now.Year()

	// 秒字段匹配（5 字段模式默认 "0"）
	secField := sched.Second
	if secField == "" {
		secField = "0"
	}
	if !cm.matchFieldExtended(secField, second, 0, 59, now, false) {
		return false
	}
	if !cm.matchFieldExtended(sched.Minute, minute, 0, 59, now, false) {
		return false
	}
	if !cm.matchFieldExtended(sched.Hour, hour, 0, 23, now, false) {
		return false
	}
	if !cm.matchFieldExtended(sched.Day, day, 1, 31, now, true) {
		return false
	}
	if !cm.matchFieldExtended(sched.Month, month, 1, 12, now, false) {
		return false
	}
	if !cm.matchFieldExtended(sched.Weekday, weekday, 1, 7, now, false) {
		return false
	}
	// 年字段匹配（5/6 字段模式默认 "*"）
	yearField := sched.Year
	if yearField == "" || yearField == "*" {
		return true
	}
	return cm.matchFieldExtended(yearField, year, 1970, 2100, now, false)
}

// matchCronField checks if a cron field matches the given value
func (cm *CronManager) matchCronField(field string, value, minVal, maxVal int) bool {
	if field == "*" {
		return true
	}

	// handle step syntax: */N
	if strings.HasPrefix(field, "*/") {
		step, err := strconv.Atoi(field[2:])
		if err != nil || step <= 0 {
			return false
		}
		return value%step == 0
	}

	// handle range syntax: N-M
	if strings.Contains(field, "-") {
		parts := strings.SplitN(field, "-", 2)
		start, err1 := strconv.Atoi(parts[0])
		end, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return false
		}
		return value >= start && value <= end
	}

	// handle list syntax: N,M,K
	if strings.Contains(field, ",") {
		for _, part := range strings.Split(field, ",") {
			v, err := strconv.Atoi(strings.TrimSpace(part))
			if err == nil && v == value {
				return true
			}
		}
		return false
	}

	// handle single value
	v, err := strconv.Atoi(field)
	if err != nil {
		return false
	}
	return v == value
}

// ValidateCronSchedule validates a 5/6/7-field cron expression（M08-03 改造，）
// - 5 字段：min hour day month weekday（向后兼容）
// - 6 字段：sec min hour day month weekday
// - 7 字段：sec min hour day month weekday year
// - 支持 L/W/LW/# 修饰符
func ValidateCronSchedule(schedule string) error {
	sched, err := ParseCronSchedule(schedule)
	if err != nil {
		return err
	}
	// 字段范围表
	type fieldRange struct{ min, max int }
	ranges := map[string]fieldRange{
		"second":  {0, 59},
		"minute":  {0, 59},
		"hour":    {0, 23},
		"day":     {1, 31},
		"month":   {1, 12},
		"weekday": {1, 7},
		"year":    {1970, 2100},
	}
	// 秒字段（6/7 字段模式）
	if sched.FieldCount >= Cron6Fields {
		if err := validateCronFieldExtended(sched.Second, ranges["second"].min, ranges["second"].max); err != nil {
			return fmt.Errorf("second field (%s): %w", sched.Second, err)
		}
	}
	if err := validateCronFieldExtended(sched.Minute, ranges["minute"].min, ranges["minute"].max); err != nil {
		return fmt.Errorf("minute field (%s): %w", sched.Minute, err)
	}
	if err := validateCronFieldExtended(sched.Hour, ranges["hour"].min, ranges["hour"].max); err != nil {
		return fmt.Errorf("hour field (%s): %w", sched.Hour, err)
	}
	if err := validateCronFieldExtended(sched.Day, ranges["day"].min, ranges["day"].max); err != nil {
		return fmt.Errorf("day field (%s): %w", sched.Day, err)
	}
	if err := validateCronFieldExtended(sched.Month, ranges["month"].min, ranges["month"].max); err != nil {
		return fmt.Errorf("month field (%s): %w", sched.Month, err)
	}
	if err := validateCronFieldExtended(sched.Weekday, ranges["weekday"].min, ranges["weekday"].max); err != nil {
		return fmt.Errorf("weekday field (%s): %w", sched.Weekday, err)
	}
	// 年字段（7 字段模式）
	if sched.FieldCount == Cron7Fields {
		if err := validateCronFieldExtended(sched.Year, ranges["year"].min, ranges["year"].max); err != nil {
			return fmt.Errorf("year field (%s): %w", sched.Year, err)
		}
	}
	return nil
}

// validateCronFieldExtended 扩展字段验证，允许 L/W/LW/# 修饰符（M08-03 改造，）
// - L：最后一个（如 day=L 表示月末；weekday=5L 表示最后一个周五）
// - LW：月末最近工作日
// - 15W：离 15 号最近的工作日
// - 5#3：第 3 个周五
// - ?：与 * 等价（Quartz 不指定语义）
func validateCronFieldExtended(field string, minVal, maxVal int) error {
	if field == "*" || field == "?" {
		return nil
	}
	// L / LW 修饰符
	if field == "L" || field == "LW" {
		return nil
	}
	// 5L / 6L 形式（最后一个周 X）
	if strings.HasSuffix(field, "L") && len(field) > 1 {
		prefix := field[:len(field)-1]
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("invalid L modifier: %s", field)
		}
		if v < minVal || v > maxVal {
			return fmt.Errorf("L modifier value %d out of bounds [%d-%d]", v, minVal, maxVal)
		}
		return nil
	}
	// 15W 形式（最近工作日）
	if strings.HasSuffix(field, "W") && len(field) > 1 {
		prefix := field[:len(field)-1]
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("invalid W modifier: %s", field)
		}
		if v < minVal || v > maxVal {
			return fmt.Errorf("W modifier value %d out of bounds [%d-%d]", v, minVal, maxVal)
		}
		return nil
	}
	// 5#3 形式（第 N 个星期 X）
	if strings.Contains(field, "#") {
		parts := strings.SplitN(field, "#", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid # modifier: %s", field)
		}
		v1, err1 := strconv.Atoi(parts[0])
		v2, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return fmt.Errorf("invalid # modifier: %s", field)
		}
		if v1 < minVal || v1 > maxVal {
			return fmt.Errorf("# modifier weekday %d out of bounds [%d-%d]", v1, minVal, maxVal)
		}
		if v2 < 1 || v2 > 5 {
			return fmt.Errorf("# modifier nth %d out of bounds [1-5]", v2)
		}
		return nil
	}
	// 走原 validateCronField 逻辑
	return validateCronField(field, minVal, maxVal)
}

// validateCronField validates a single cron field value
func validateCronField(field string, minVal, maxVal int) error {
	if strings.HasPrefix(field, "*/") {
		step, err := strconv.Atoi(field[2:])
		if err != nil || step <= 0 {
			return fmt.Errorf("invalid step value: %s", field)
		}
		return nil
	}

	if strings.Contains(field, "-") {
		parts := strings.SplitN(field, "-", 2)
		start, err1 := strconv.Atoi(parts[0])
		end, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return fmt.Errorf("invalid range: %s", field)
		}
		if start < minVal || end > maxVal || start > end {
			return fmt.Errorf("range %s out of bounds [%d-%d]", field, minVal, maxVal)
		}
		return nil
	}

	if strings.Contains(field, ",") {
		for _, part := range strings.Split(field, ",") {
			v, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return fmt.Errorf("invalid value: %s", part)
			}
			if v < minVal || v > maxVal {
				return fmt.Errorf("value %d out of bounds [%d-%d]", v, minVal, maxVal)
			}
		}
		return nil
	}

	v, err := strconv.Atoi(field)
	if err != nil {
		return fmt.Errorf("invalid value: %s", field)
	}
	if v < minVal || v > maxVal {
		return fmt.Errorf("value %d out of bounds [%d-%d]", v, minVal, maxVal)
	}
	return nil
}

// Create creates a cron job with schedule validation
func (cm *CronManager) Create(schedule, command, description string) (*CronJob, error) {
	if err := ValidateCronSchedule(schedule); err != nil {
		return nil, fmt.Errorf("invalid cron schedule: %w", err)
	}

	id := fmt.Sprintf("cron_%d", cm.nextID.Add(1))
	job := &CronJob{
		ID:          id,
		Schedule:    schedule,
		Command:     command,
		Description: description,
		CreatedAt:   time.Now(),
		Status:      "active",
	}
	cm.mu.Lock()
	cm.jobs[id] = job
	cm.mu.Unlock()

	log.Printf("[CronManager] created job %s: %s %s", id, schedule, command)
	return job, nil
}

// List lists cron jobs
func (cm *CronManager) List() []*CronJob {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	var result []*CronJob
	for _, job := range cm.jobs {
		if job.Status == "active" {
			result = append(result, job)
		}
	}
	return result
}

// Delete deletes a cron job. Also triggers cleanup of previously deleted jobs.
func (cm *CronManager) Delete(id string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	job, ok := cm.jobs[id]
	if !ok {
		return fmt.Errorf("cron job %s does not exist", id)
	}
	if job.Status == "deleted" {
		return fmt.Errorf("cron job %s already deleted", id)
	}
	job.Status = "deleted"
	// Clean up deleted jobs from the map
	for jid, j := range cm.jobs {
		if j.Status == "deleted" {
			delete(cm.jobs, jid)
		}
	}
	log.Printf("[CronManager] deleted job %s", id)
	return nil
}

func (cm *CronManager) CleanDeleted() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	count := 0
	for id, job := range cm.jobs {
		if job.Status == "deleted" {
			delete(cm.jobs, id)
			count++
		}
	}
	return count
}

// cronListHandler CronList tool handler
func (e *Executor) cronListHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		jobs := e.CronMgr.List()
		if len(jobs) == 0 {
			return util.Bi("No scheduled tasks", "没有定时任务"), nil
		}

		var results []string
		for _, j := range jobs {
			results = append(results, fmt.Sprintf("%s [%s] %s — %s (created at %s)",
				j.ID, j.Schedule, j.Command, j.Description,
				j.CreatedAt.Format("2006-01-02 15:04:05")))
		}
		return fmt.Sprintf("cron job list (%d):\n%s", len(jobs), joinLines(results)), nil
	}
}

// cronDeleteHandler CronDelete tool handler
func (e *Executor) cronDeleteHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			JobID string `json:"job_id"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.JobID == "" {
			return "", fmt.Errorf("job_id cannot be empty")
		}

		if err := e.CronMgr.Delete(input.JobID); err != nil {
			return "", err
		}
		return fmt.Sprintf("cron job %s deleted", input.JobID), nil
	}
}

// ============================================================
// M08-03 Cron 7 字段扩展
// 支持 5/6/7 字段表达式 + L/W/LW/# 修饰符（对标 Quartz CronExpression）
// ============================================================

// CronFieldCount 标识 cron 表达式字段数（M08-03 改造，）
type CronFieldCount int

const (
	// Cron5Fields 标准 5 字段（分时日月周），向后兼容
	Cron5Fields CronFieldCount = 5
	// Cron6Fields 6 字段（秒分时日月周），支持秒级精度
	Cron6Fields CronFieldCount = 6
	// Cron7Fields 7 字段（秒分时日月周年），支持年字段
	Cron7Fields CronFieldCount = 7
)

// CronSchedule 表示解析后的 cron 表达式（M08-03 改造，）
// 对标 Quartz CronExpression：支持 5/6/7 字段自适应
type CronSchedule struct {
	Second     string         // 秒字段（5 字段模式为 ""，匹配时默认 "0"）
	Minute     string         // 分字段
	Hour       string         // 时字段
	Day        string         // 日字段（支持 L/W/LW 修饰符）
	Month      string         // 月字段
	Weekday    string         // 周字段（支持 # 和 5L 修饰符）
	Year       string         // 年字段（5/6 字段模式为 ""，匹配时默认 "*"）
	FieldCount CronFieldCount // 原始字段数（5/6/7）
}

// ParseCronSchedule 解析 5/6/7 字段 cron 表达式（M08-03 改造，）
// - 5 字段：min hour day month weekday（向后兼容）
// - 6 字段：sec min hour day month weekday
// - 7 字段：sec min hour day month weekday year
// - 其他字段数：返回错误
func ParseCronSchedule(schedule string) (*CronSchedule, error) {
	fields := strings.Fields(schedule)
	switch len(fields) {
	case 5:
		return &CronSchedule{
			Minute:     fields[0],
			Hour:       fields[1],
			Day:        fields[2],
			Month:      fields[3],
			Weekday:    fields[4],
			FieldCount: Cron5Fields,
		}, nil
	case 6:
		return &CronSchedule{
			Second:     fields[0],
			Minute:     fields[1],
			Hour:       fields[2],
			Day:        fields[3],
			Month:      fields[4],
			Weekday:    fields[5],
			FieldCount: Cron6Fields,
		}, nil
	case 7:
		return &CronSchedule{
			Second:     fields[0],
			Minute:     fields[1],
			Hour:       fields[2],
			Day:        fields[3],
			Month:      fields[4],
			Weekday:    fields[5],
			Year:       fields[6],
			FieldCount: Cron7Fields,
		}, nil
	default:
		return nil, fmt.Errorf("cron expression must have 5-7 fields, got %d", len(fields))
	}
}

// lastDayOfMonth 返回指定年月的天数（处理闰年，OPT-06 强制落实）
// year=2026, month=2 → 28；year=2024, month=2 → 29
// 实现思路：用 time.Date(year, month+1, 0, ...) 取 month 的最后一天
func lastDayOfMonth(year int, month time.Month) int {
	// time.Date 会对 day=0 自动回退到上月最后一天
	// month+1 = 下月，day=0 = 下月最后一天的前一天 = 本月最后一天
	firstOfNextMonth := time.Date(year, month+1, 1, 0, 0, 0, 0, time.Local)
	return firstOfNextMonth.AddDate(0, 0, -1).Day()
}

// matchFieldExtended 扩展字段匹配，支持 L/W/LW/# 修饰符（M08-03 改造，）
// - L：最后一个（如 day=L 表示月末；weekday=5L 表示最后一个周五）
// - W：最近工作日（如 15W 表示离 15 号最近的工作日）
// - LW：月末最近工作日
// - #：第 N 个星期 X（如 5#3 表示第 3 个周五）
// - ?：与 * 等价（Quartz 不指定语义，OPT-03）
// - 其他走原 matchCronField 逻辑
//
// 参数 isDayField 标识当前是否为日字段（仅日字段支持 L/W/LW）
func (cm *CronManager) matchFieldExtended(field string, value, minVal, maxVal int, now time.Time, isDayField bool) bool {
	// ? 与 * 等价（Quartz 语义）
	if field == "?" {
		field = "*"
	}
	// L 修饰符（仅日字段合法）
	if field == "L" {
		if isDayField {
			// day=L：当前 day 必须为月末
			return value == lastDayOfMonth(now.Year(), now.Month())
		}
		// weekday=L 不合法（应为 5L 形式），降级到普通匹配
		return cm.matchCronField(field, value, minVal, maxVal)
	}
	// LW 修饰符（仅日字段合法：月末最近工作日）
	if field == "LW" && isDayField {
		lastDay := lastDayOfMonth(now.Year(), now.Month())
		return value == nearestWeekday(lastDay, now.Year(), now.Month())
	}
	// 5L 形式（weekday 字段：最后一个周 X）
	if strings.HasSuffix(field, "L") && len(field) > 1 {
		prefix := field[:len(field)-1]
		targetWeekday, err := strconv.Atoi(prefix)
		if err != nil {
			return false
		}
		// 当前 weekday 必须为最后一个该 weekday
		return value == targetWeekday && isLastWeekdayInMonth(now, targetWeekday)
	}
	// 15W 形式（day 字段：最近工作日）
	if strings.HasSuffix(field, "W") && len(field) > 1 {
		prefix := field[:len(field)-1]
		targetDay, err := strconv.Atoi(prefix)
		if err != nil {
			return false
		}
		return value == nearestWeekday(targetDay, now.Year(), now.Month())
	}
	// 5#3 形式（weekday 字段：第 N 个星期 X）
	if strings.Contains(field, "#") {
		parts := strings.SplitN(field, "#", 2)
		if len(parts) != 2 {
			return false
		}
		targetWeekday, err1 := strconv.Atoi(parts[0])
		targetNth, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return false
		}
		return value == targetWeekday && nthWeekdayInMonth(now, targetWeekday, targetNth)
	}
	// 走原 matchCronField 逻辑
	return cm.matchCronField(field, value, minVal, maxVal)
}

// nearestWeekday 返回指定 day 的最近工作日（周一至周五，M08-03 改造，）
// 如 15 号是周六，返回 14（周五）；15 号是周日，返回 16（周一）
// 跨月时取月内最近的工作日（如 1 号是周六，返回 3 = 周一；月末周日返回 lastDay-2 = 上周五）
func nearestWeekday(day int, year int, month time.Month) int {
	target := time.Date(year, month, day, 0, 0, 0, 0, time.Local)
	weekday := int(target.Weekday())
	if weekday == 0 { // 周日 → +1 到周一
		target = target.AddDate(0, 0, 1)
	} else if weekday == 6 { // 周六 → -1 到周五
		target = target.AddDate(0, 0, -1)
	}
	// 跨月守卫：若调整后跨月，取月内最近的工作日
	// 触发场景仅两种：day=1 周六（-1 跨月）或 day=lastDay 周日（+1 跨月）
	if target.Month() != month {
		if day == 1 {
			// 月初周六 → -1 跨月，取下周一（day=3）
			target = time.Date(year, month, 1, 0, 0, 0, 0, time.Local).AddDate(0, 0, 2)
		} else {
			// 月末周日 → +1 跨月，取本月最后一个周五（lastDay-2）
			// 注：lastDay 必为周日（仅周日 +1 才跨月），故 -2 = 周五
			lastDay := lastDayOfMonth(year, month)
			target = time.Date(year, month, lastDay, 0, 0, 0, 0, time.Local).AddDate(0, 0, -2)
		}
	}
	return target.Day()
}

// isLastWeekdayInMonth 判断当前时间是否为本月最后一个指定 weekday（M08-03 改造，）
// 如 2026-07-21 是 7 月最后一个周一（如果 7 月 28 日不是周一）
// targetWeekday 用 cron 数字（1-7 表示周一到周日，7=周日）
func isLastWeekdayInMonth(now time.Time, targetWeekday int) bool {
	// 转换：Go time.Weekday() 周日=0，cron 周日=7
	if targetWeekday == 7 {
		targetWeekday = 0
	}
	lastDay := lastDayOfMonth(now.Year(), now.Month())
	// 从月末向前找最近的 targetWeekday
	lastDate := time.Date(now.Year(), now.Month(), lastDay, 0, 0, 0, 0, time.Local)
	for lastDate.Weekday() != time.Weekday(targetWeekday) {
		lastDate = lastDate.AddDate(0, 0, -1)
	}
	return now.Day() == lastDate.Day()
}

// nthWeekdayInMonth 判断当前时间是否为本月第 N 个指定 weekday（M08-03 改造，）
// 如 5#3 表示本月第 3 个周五
// targetWeekday 用 cron 数字（1-7 表示周一到周日，7=周日）
// nth 范围 1-5，超出返回 false
func nthWeekdayInMonth(now time.Time, targetWeekday, nth int) bool {
	if targetWeekday == 7 {
		targetWeekday = 0
	}
	if nth < 1 || nth > 5 {
		return false
	}
	// 计算本月第 N 个 targetWeekday 的日期
	firstDay := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	offset := (targetWeekday - int(firstDay.Weekday()) + 7) % 7
	targetDate := firstDay.AddDate(0, 0, offset+(nth-1)*7)
	// 跨月守卫：第 N 个可能超出本月（如 2 月没有第 5 个周一）
	if targetDate.Month() != now.Month() {
		return false
	}
	return now.Day() == targetDate.Day()
}
