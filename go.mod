module github.com/anchorandfish/whale-code

go 1.26.10

require (
	github.com/bmatcuk/doublestar/v4 v4.10.0
	github.com/charmbracelet/bubbles v0.20.0
	github.com/charmbracelet/bubbletea v1.2.4
	github.com/charmbracelet/glamour v1.0.0
	github.com/charmbracelet/lipgloss v1.1.1-0.20250404203927-76690c660834
	github.com/charmbracelet/x/cellbuf v0.0.13
	github.com/fsnotify/fsnotify v1.7.0 // M16-02：钩子配置热重载（fsnotify 文件监听）
	github.com/gorilla/websocket v1.5.3
	github.com/hashicorp/golang-lru/v2 v2.0.7 // N13 ：L0 引擎文本测量 LRU 缓存（direct）
	github.com/prometheus/client_golang v1.22.0
	github.com/sabhiram/go-gitignore v0.0.0-20210923224102-525f6e181f06
	github.com/sahilm/fuzzy v0.1.1 // N5 ：fzf 算法纯 Go 移植（本地 replace）
	github.com/sergi/go-diff v1.3.1 // N9 ：字符级 Myers diff（行级用 Levenshtein DP）
	golang.org/x/mod v0.41.0 // V3-1-2：版本号 semver 对比（doctor --update-check）
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0 // N18：Onboarding API key 输入（term.ReadPassword 不回显）
	golang.org/x/text v0.43.0
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/mattn/go-runewidth v0.0.17 // N13 ：L0 引擎文本测量（direct）
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.63.0 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	google.golang.org/protobuf v1.36.8 // indirect
)

require (
	github.com/charmbracelet/x/ansi v0.10.2
	github.com/muesli/termenv v0.16.0
	github.com/pkoukk/tiktoken-go v0.1.8
	github.com/rivo/uniseg v0.4.7
	golang.org/x/image v0.45.0
	golang.org/x/net v0.62.0
	modernc.org/sqlite v1.54.0
)

require (
	github.com/alecthomas/chroma/v2 v2.20.0 // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.2.3-0.20250311203215-f60798e515dc // indirect
	github.com/charmbracelet/x/exp/slice v0.0.0-20250327172914-2fdc97757edf // indirect
	github.com/charmbracelet/x/term v0.2.1 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/klauspost/compress v1.18.7 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/reflow v0.3.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yuin/goldmark v1.7.17 // indirect
	github.com/yuin/goldmark-emoji v1.0.6 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

// N5 ：sahilm/fuzzy 本地 replace（本机 Winsock 损坏无法 go get）
replace github.com/sahilm/fuzzy => ./third_party/sahilm/fuzzy
