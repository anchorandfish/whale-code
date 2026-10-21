package daemon

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
	_ "modernc.org/sqlite"
)

var taskStoreOpenMu sync.Mutex

const (
	taskSchemaVersion        = 1
	terminalTaskRetention    = 30 * 24 * time.Hour
	maxRetainedTerminalTasks = 10000
	defaultTaskPageSize      = 100
	maxTaskPageSize          = 500
)

type taskStore struct {
	db         *sql.DB
	dbPath     string
	legacyPath string
}

type taskCursor struct {
	CreatedAt int64  `json:"created_at"`
	ID        string `json:"id"`
}

type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func newTaskStore(legacyPath string) *taskStore {
	dbPath := legacyPath
	if strings.EqualFold(filepath.Ext(legacyPath), ".json") {
		dbPath = strings.TrimSuffix(legacyPath, filepath.Ext(legacyPath)) + ".db"
	}
	return &taskStore{dbPath: dbPath, legacyPath: legacyPath}
}

func (s *taskStore) open(ctx context.Context) error {
	if s.db != nil {
		return nil
	}
	taskStoreOpenMu.Lock()
	defer taskStoreOpenMu.Unlock()
	if s.db != nil {
		return nil
	}
	if strings.TrimSpace(s.dbPath) == "" {
		return errors.New("task database path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0700); err != nil {
		return fmt.Errorf("create task database directory: %w", err)
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return fmt.Errorf("open task database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = db.Close()
		}
	}()
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure task database (%s): %w", pragma, err)
		}
	}
	s.db = db
	if err := s.migrate(ctx); err != nil {
		s.db = nil
		return err
	}
	if err := s.importLegacyJSON(ctx); err != nil {
		s.db = nil
		return err
	}
	if err := os.Chmod(s.dbPath, 0600); err != nil {
		s.db = nil
		return fmt.Errorf("restrict task database permissions: %w", err)
	}
	closeOnError = false
	return nil
}

func (s *taskStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *taskStore) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task schema migration: %w", err)
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS daemon_schema (
			component TEXT PRIMARY KEY,
			version INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS daemon_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			prompt TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed','cancelled')),
			work_dir TEXT NOT NULL,
			result TEXT NOT NULL,
			exit_code INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			started_at INTEGER,
			completed_at INTEGER,
			type TEXT NOT NULL,
			priority INTEGER NOT NULL,
			progress INTEGER NOT NULL CHECK (progress BETWEEN 0 AND 100),
			partial_output TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_schedule ON tasks(status, priority DESC, created_at ASC, id ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_history ON tasks(created_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_retention ON tasks(status, completed_at)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply task schema: %w", err)
		}
	}
	var version int
	err = tx.QueryRowContext(ctx, `SELECT version FROM daemon_schema WHERE component = 'tasks'`).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read task schema version: %w", err)
	}
	if version > taskSchemaVersion {
		return fmt.Errorf("task database schema %d is newer than supported version %d", version, taskSchemaVersion)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO daemon_schema(component, version) VALUES('tasks', ?)
		ON CONFLICT(component) DO UPDATE SET version = excluded.version`, taskSchemaVersion); err != nil {
		return fmt.Errorf("write task schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task schema migration: %w", err)
	}
	return nil
}

func (s *taskStore) importLegacyJSON(ctx context.Context) error {
	if s.legacyPath == "" || s.legacyPath == s.dbPath {
		return nil
	}
	var imported string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM daemon_metadata WHERE key = 'legacy_json_imported'`).Scan(&imported)
	if err == nil {
		return s.writeLegacyMarker()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read legacy migration state: %w", err)
	}

	data, readErr := os.ReadFile(s.legacyPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read legacy task file: %w", readErr)
	}
	var tasks []Task
	if readErr == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &tasks); err != nil {
			return fmt.Errorf("deserialize legacy tasks: %w", err)
		}
		backupPath := s.legacyPath + ".pre-sqlite.bak"
		if _, err := os.Stat(backupPath); os.IsNotExist(err) {
			if err := util.AtomicWriteFile(backupPath, data, 0600); err != nil {
				return fmt.Errorf("back up legacy tasks: %w", err)
			}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy task import: %w", err)
	}
	defer tx.Rollback()
	for i := range tasks {
		if err := insertTask(ctx, tx, tasks[i]); err != nil {
			return fmt.Errorf("import legacy task %s: %w", tasks[i].ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO daemon_metadata(key, value) VALUES('legacy_json_imported', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record legacy task import: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy task import: %w", err)
	}

	return s.writeLegacyMarker()
}

func (s *taskStore) writeLegacyMarker() error {
	if existing, err := os.ReadFile(s.legacyPath); err == nil {
		var current struct {
			MigratedTo    string `json:"migrated_to"`
			SchemaVersion int    `json:"schema_version"`
		}
		if json.Unmarshal(existing, &current) == nil && current.MigratedTo == filepath.Base(s.dbPath) && current.SchemaVersion == taskSchemaVersion {
			return nil
		}
	}
	marker, _ := json.MarshalIndent(map[string]interface{}{
		"migrated_to":    filepath.Base(s.dbPath),
		"schema_version": taskSchemaVersion,
	}, "", "  ")
	if err := util.AtomicWriteFile(s.legacyPath, marker, 0600); err != nil {
		return fmt.Errorf("write legacy migration marker: %w", err)
	}
	return nil
}

func (s *taskStore) replaceAll(ctx context.Context, tasks []Task) error {
	if err := s.open(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks`); err != nil {
		return err
	}
	for i := range tasks {
		if err := insertTask(ctx, tx, tasks[i]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *taskStore) insert(ctx context.Context, task Task) error {
	if err := s.open(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertTask(ctx, tx, task); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *taskStore) update(ctx context.Context, task Task, expected TaskStatus) error {
	if err := s.open(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	args := taskValues(task, false)
	args = append(args, task.ID, string(expected))
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET
		prompt=?, status=?, work_dir=?, result=?, exit_code=?, created_at=?, started_at=?, completed_at=?, type=?, priority=?, progress=?, partial_output=?
		WHERE id=? AND status=?`, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("task %s was concurrently modified", task.ID)
	}
	return tx.Commit()
}

func insertTask(ctx context.Context, tx *sql.Tx, task Task) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tasks(
		id, prompt, status, work_dir, result, exit_code, created_at, started_at, completed_at, type, priority, progress, partial_output
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, taskValues(task, true)...)
	return err
}

func taskValues(task Task, includeIDFirst bool) []interface{} {
	values := []interface{}{
		task.Prompt, string(task.Status), task.WorkDir, task.Result, task.ExitCode,
		task.CreatedAt.UTC().UnixNano(), nullableTime(task.StartedAt), nullableTime(task.CompletedAt),
		string(task.Type), int(task.Priority), task.Progress, task.PartialOutput,
	}
	if includeIDFirst {
		return append([]interface{}{task.ID}, values...)
	}
	return values
}

func nullableTime(value *time.Time) interface{} {
	if value == nil {
		return nil
	}
	return value.UTC().UnixNano()
}

func (s *taskStore) load(ctx context.Context) ([]Task, error) {
	if err := s.open(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, prompt, status, work_dir, result, exit_code,
		created_at, started_at, completed_at, type, priority, progress, partial_output
		FROM tasks ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func scanTasks(rows *sql.Rows) ([]Task, error) {
	tasks := make([]Task, 0)
	for rows.Next() {
		var task Task
		var status, taskType string
		var created int64
		var started, completed sql.NullInt64
		if err := rows.Scan(&task.ID, &task.Prompt, &status, &task.WorkDir, &task.Result, &task.ExitCode,
			&created, &started, &completed, &taskType, &task.Priority, &task.Progress, &task.PartialOutput); err != nil {
			return nil, err
		}
		task.Status = TaskStatus(status)
		task.Type = TaskType(taskType)
		task.CreatedAt = time.Unix(0, created)
		if started.Valid {
			value := time.Unix(0, started.Int64)
			task.StartedAt = &value
		}
		if completed.Valid {
			value := time.Unix(0, completed.Int64)
			task.CompletedAt = &value
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *taskStore) recoverInterrupted(ctx context.Context, now time.Time) error {
	if err := s.open(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status='failed', result=?, exit_code=-1, completed_at=? WHERE status='running'`,
		"Task interrupted by daemon shutdown; review the workspace before retrying", now.UTC().UnixNano())
	return err
}

func (s *taskStore) applyRetention(ctx context.Context, now time.Time) error {
	if err := s.open(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	terminal := `status IN ('completed','failed','cancelled')`
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE `+terminal+` AND COALESCE(completed_at, created_at) < ?`, now.Add(-terminalTaskRetention).UTC().UnixNano()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id IN (
		SELECT id FROM tasks WHERE `+terminal+` ORDER BY COALESCE(completed_at, created_at) DESC, id DESC LIMIT -1 OFFSET ?
	)`, maxRetainedTerminalTasks); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *taskStore) listPage(ctx context.Context, cursor string, limit int) (TaskPage, error) {
	if err := s.open(ctx); err != nil {
		return TaskPage{}, err
	}
	if limit <= 0 {
		limit = defaultTaskPageSize
	}
	if limit > maxTaskPageSize {
		limit = maxTaskPageSize
	}
	query := `SELECT id, prompt, status, work_dir, result, exit_code, created_at, started_at, completed_at, type, priority, progress, partial_output FROM tasks`
	args := make([]interface{}, 0, 3)
	if cursor != "" {
		decoded, err := decodeTaskCursor(cursor)
		if err != nil {
			return TaskPage{}, err
		}
		query += ` WHERE created_at < ? OR (created_at = ? AND id < ?)`
		args = append(args, decoded.CreatedAt, decoded.CreatedAt, decoded.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return TaskPage{}, err
	}
	defer rows.Close()
	tasks, err := scanTasks(rows)
	if err != nil {
		return TaskPage{}, err
	}
	page := TaskPage{Tasks: tasks}
	if len(tasks) > limit {
		page.Tasks = tasks[:limit]
		last := page.Tasks[len(page.Tasks)-1]
		page.NextCursor = encodeTaskCursor(taskCursor{CreatedAt: last.CreatedAt.UTC().UnixNano(), ID: last.ID})
	}
	return page, nil
}

func encodeTaskCursor(cursor taskCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeTaskCursor(value string) (taskCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return taskCursor{}, fmt.Errorf("invalid task cursor: %w", err)
	}
	var cursor taskCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.ID == "" {
		return taskCursor{}, errors.New("invalid task cursor")
	}
	return cursor, nil
}
