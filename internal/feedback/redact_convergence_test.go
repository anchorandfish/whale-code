package feedback

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRedact_NoSecondSecretPatternTable 是「脱敏规则单一权威」的**永久结构门**。
//
// （方案 A）收口后，密钥脱敏正则的**唯一声明处**是 internal/secret。
// 本门从源码面禁止任何非 secret 包再次声明一张密钥脱敏正则表
// （特征：regexp.MustCompile 且含密钥前缀字面量）——
// 这正是 feedback 曾经自持 8 条副本、缺 gcp/pem 导致明文外发的根因。
//
// ★ 判别力自证：若有人把 feedback.redactSensitive 改回本地正则表
//
//	（哪怕抄得与 secret 一模一样），本门立即变红。
//
// 只扫生产文件（跳过 _test.go）与 internal/ 下非 secret 的包，避免误伤
// secret 自身（SSOT）与确实需要自带正则的模块（如 bash 的 shell-quote 检测）。
func TestRedact_NoSecondSecretPatternTable(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件路径")
	}
	feedbackDir := filepath.Dir(thisFile)     // .../internal/feedback
	internalRoot := filepath.Dir(feedbackDir) // .../internal
	secretDir := filepath.Join(internalRoot, "secret")

	// 密钥脱敏正则的强特征串：几乎不可能出现在非脱敏代码里。
	// 每条都对应 secret/redact.go 中一条规则的前缀。
	forbidden := []string{
		"regexp.MustCompile(`(ghp|gho|ghu|ghs|ghr)_", // GitHub token
		"regexp.MustCompile(`\\b(AKIA|ASIA",          // AWS access key
		"regexp.MustCompile(`AIza",                   // GCP api key
		"PRIVATE KEY-----`",                          // PEM private key
	}

	absSecret, err := filepath.Abs(secretDir)
	if err != nil {
		t.Fatalf("定位 secret 目录失败：%v", err)
	}

	// 遍历 internal/ 下所有 .go 生产文件（跳过 secret 包与 _test.go）
	violations := []string{}
	err = filepath.Walk(internalRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// 跳过 secret 包自身（它是 SSOT）
			absPath, aerr := filepath.Abs(path)
			if aerr == nil && absPath == absSecret {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(data)
		if !strings.Contains(src, "regexp.MustCompile") {
			return nil
		}
		for _, f := range forbidden {
			if strings.Contains(src, f) {
				violations = append(violations, path+" 中出现特征串 "+f)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 internal/ 失败：%v", err)
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Errorf("第二张密钥脱敏正则表复活：%s —— 脱敏规则唯一声明处是 internal/secret（ 方案 A）", v)
		}
	}
}
