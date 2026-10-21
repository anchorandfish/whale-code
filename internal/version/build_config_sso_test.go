package version

import (
	"os"
	"regexp"
	"testing"
)

// 构建配置里的版本号漂移防护（：同一事实跨入口必须同源）。
//
// 真实发生过的事故形状：源码 internal/version.Version = "0.1.0"、`whale --version`
// 输出 0.1.0，但用 `make build` 构建出的二进制自称 "dev" —— 因为 Makefile 里写着
// `VERSION ?= dev`，脚本里写着 `VERSION="${1:-dev}"`，链接期把源码里的值覆盖掉了。
// 只测 Go 代码查不出这类问题：漂移发生在「构建配置」这一层，必须把构建配置也纳入判据。
//
// 判据两条：
// 1. 构建脚本不得给版本号写死默认字面量（不传参时应让源码值原样生效）；
// 2. 构建脚本注入的必须是 internal/version 这个包的 Version（唯一真源），
// 而不是某个别处的变量。
func TestBuildScriptsDoNotHardcodeVersion(t *testing.T) {
	files := []string{
		"../../Makefile",
		"../../scripts/build.sh",
		"../../scripts/build.bat",
	}

	// 三种「默认值被钉死成字面量」的写法。
	hardcoded := []struct {
		what string
		re   *regexp.Regexp
	}{
		// Makefile: `VERSION ?= dev`（空值 `VERSION ?=` 是合法的，表示不注入）
		{"Makefile 的 VERSION 默认值", regexp.MustCompile(`(?m)^\s*VERSION\s*\?=[ \t]*[^\s#]`)},
		// build.sh: `${1:-dev}`（`${1:-}` 是合法的，表示不注入）
		{"shell 脚本的 ${1:-…} 默认值", regexp.MustCompile(`\$\{1:-[^}]+\}`)},
		// build.bat: `set "VERSION=dev"`（`set "VERSION=%~1"` 是读入参，不算）
		{"batch 脚本的 VERSION 默认值", regexp.MustCompile(`(?i)set\s+"VERSION=[^"%]+"`)},
	}

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			// 导出/裁剪后的树可能没有这些文件；缺文件不构成版本漂移证据，跳过而非假绿。
			t.Skipf("跳过：读不到 %s（%v）", f, err)
		}
		src := string(data)

		for _, h := range hardcoded {
			if loc := h.re.FindStringIndex(src); loc != nil {
				t.Errorf("%s 里存在写死的版本号默认值：%q\n"+
					"这会覆盖 internal/version.Version，让构建产物与源码自称不同的版本。\n"+
					"改成不注入（Makefile 用 `VERSION ?=`，shell 用 `${1:-}`），把版本交给源码真源。",
					f, src[loc[0]:loc[1]])
			}
		}

		if !regexp.MustCompile(`internal/version`).MatchString(src) {
			t.Errorf("%s 没有引用 internal/version —— 版本号必须注入到唯一真源那个包", f)
		}
	}
}
