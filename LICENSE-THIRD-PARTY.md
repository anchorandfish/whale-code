# Third Party Licenses

> 手动生成（基于 go.mod 依赖清单）
> 生成方式：`scripts/license-audit.sh`（go-licenses 工具）。构建环境因网络限制（golang.org 不可达）导致 go-licenses URL 发现超时，未能自动完成，故基于 go.mod 手动整理。后续在可联网环境运行 `bash scripts/license-audit.sh` 可自动重新生成。
> Whale Code 使用以下开源库（direct + indirect 依赖）：

## Direct Dependencies

| Package | License | Source |
|---------|---------|--------|
| github.com/bmatcuk/doublestar/v4 | MIT | https://github.com/bmatcuk/doublestar |
| github.com/charmbracelet/bubbles | MIT | https://github.com/charmbracelet/bubbles |
| github.com/charmbracelet/bubbletea | MIT | https://github.com/charmbracelet/bubbletea |
| github.com/charmbracelet/glamour | MIT | https://github.com/charmbracelet/glamour |
| github.com/charmbracelet/lipgloss | MIT | https://github.com/charmbracelet/lipgloss |
| github.com/charmbracelet/x/cellbuf | MIT | https://github.com/charmbracelet/x |
| github.com/fsnotify/fsnotify | BSD-3-Clause | https://github.com/fsnotify/fsnotify |
| github.com/gorilla/websocket | BSD-2-Clause | https://github.com/gorilla/websocket |
| github.com/hashicorp/golang-lru/v2 | MPL-2.0 | https://github.com/hashicorp/golang-lru |
| github.com/prometheus/client_golang | Apache-2.0 | https://github.com/prometheus/client_golang |
| github.com/sabhiram/go-gitignore | MIT | https://github.com/sabhiram/go-gitignore |
| github.com/sahilm/fuzzy | MIT | https://github.com/sahilm/fuzzy (本地 replace：./third_party/sahilm/fuzzy) |
| github.com/sergi/go-diff | MPL-2.0 | https://github.com/sergi/go-diff |
| golang.org/x/crypto | BSD-3-Clause | https://go.googlesource.com/crypto |
| golang.org/x/sync | BSD-3-Clause | https://go.googlesource.com/sync |
| golang.org/x/sys | BSD-3-Clause | https://go.googlesource.com/sys |
| golang.org/x/term | BSD-3-Clause | https://go.googlesource.com/term |
| golang.org/x/text | BSD-3-Clause | https://go.googlesource.com/text |
| gopkg.in/natefinch/lumberjack.v2 | MIT | https://github.com/natefinch/lumberjack |
| gopkg.in/yaml.v3 | Apache-2.0 | https://github.com/go-yaml/yaml |
| github.com/mattn/go-runewidth | MIT | https://github.com/mattn/go-runewidth |
| github.com/muesli/termenv | MIT | https://github.com/muesli/termenv |
| github.com/pkoukk/tiktoken-go | MIT | https://github.com/pkoukk/tiktoken-go |
| golang.org/x/image | BSD-3-Clause | https://go.googlesource.com/image |
| golang.org/x/net | BSD-3-Clause | https://go.googlesource.com/net |
| modernc.org/sqlite | BSD-3-Clause | https://gitlab.com/cznic/sqlite |

## Indirect Dependencies

| Package | License | Source |
|---------|---------|--------|
| github.com/alecthomas/chroma/v2 | MIT | https://github.com/alecthomas/chroma |
| github.com/atotto/clipboard | MIT | https://github.com/atotto/clipboard |
| github.com/aymanbagabas/go-osc52/v2 | MIT | https://github.com/aymanbagabas/go-osc52 |
| github.com/aymerick/douceur | MIT | https://github.com/aymerick/douceur |
| github.com/beorn7/perks | MIT | https://github.com/beorn7/perks |
| github.com/cespare/xxhash/v2 | MIT | https://github.com/cespare/xxhash |
| github.com/charmbracelet/colorprofile | MIT | https://github.com/charmbracelet/colorprofile |
| github.com/charmbracelet/x/ansi | MIT | https://github.com/charmbracelet/x |
| github.com/charmbracelet/x/exp/slice | MIT | https://github.com/charmbracelet/x |
| github.com/charmbracelet/x/term | MIT | https://github.com/charmbracelet/x |
| github.com/dlclark/regexp2 | MIT | https://github.com/dlclark/regexp2 |
| github.com/dustin/go-humanize | MIT | https://github.com/dustin/go-humanize |
| github.com/erikgeiser/coninput | MIT | https://github.com/erikgeiser/coninput |
| github.com/google/uuid | BSD-3-Clause | https://github.com/google/uuid |
| github.com/gorilla/css | BSD-2-Clause | https://github.com/gorilla/css |
| github.com/lucasb-eyer/go-colorful | MIT | https://github.com/lucasb-eyer/go-colorful |
| github.com/mattn/go-isatty | MIT | https://github.com/mattn/go-isatty |
| github.com/mattn/go-localereader | MIT | https://github.com/mattn/go-localereader |
| github.com/microcosm-cc/bluemonday | BSD-3-Clause | https://github.com/microcosm-cc/bluemonday |
| github.com/muesli/ansi | MIT | https://github.com/muesli/ansi |
| github.com/muesli/cancelreader | MIT | https://github.com/muesli/cancelreader |
| github.com/muesli/reflow | MIT | https://github.com/muesli/reflow |
| github.com/munnerz/goautoneg | BSD-3-Clause | https://github.com/munnerz/goautoneg |
| github.com/prometheus/client_model | Apache-2.0 | https://github.com/prometheus/client_model |
| github.com/prometheus/common | Apache-2.0 | https://github.com/prometheus/common |
| github.com/prometheus/procfs | Apache-2.0 | https://github.com/prometheus/procfs |
| github.com/ncruces/go-strftime | MIT | https://github.com/ncruces/go-strftime |
| github.com/remyoudompheng/bigfft | BSD-3-Clause | https://github.com/remyoudompheng/bigfft |
| github.com/rivo/uniseg | MIT | https://github.com/rivo/uniseg |
| github.com/xo/terminfo | MIT | https://github.com/xo/terminfo |
| github.com/yuin/goldmark | MIT | https://github.com/yuin/goldmark |
| github.com/yuin/goldmark-emoji | MIT | https://github.com/yuin/goldmark-emoji |
| golang.org/x/mod | BSD-3-Clause | https://go.googlesource.com/mod |
| google.golang.org/protobuf | BSD-3-Clause | https://github.com/protocolbuffers/protobuf-go |
| modernc.org/libc | BSD-3-Clause | https://gitlab.com/cznic/libc |
| modernc.org/mathutil | BSD-3-Clause | https://gitlab.com/cznic/mathutil |
| modernc.org/memory | BSD-3-Clause | https://gitlab.com/cznic/memory |

## License Type Summary

| License | Count | 说明 |
|---------|-------|------|
| MIT | 多数 | 宽松许可证，无 copyleft 限制 |
| BSD-2-Clause | 2 | 宽松许可证 |
| BSD-3-Clause | 多数 | 宽松许可证 |
| Apache-2.0 | 6 | 宽松许可证，含专利授权条款 |
| MPL-2.0 | 2 | 弱 copyleft（文件级），与宽松许可证兼容 |

涉及 MPL-2.0 的依赖（hashicorp/golang-lru、sergi/go-diff）为文件级弱 copyleft，不影响 Whale Code 主仓库的许可证选择，且不在高风险类别（GPL/AGPL/SSPL/BSL）内。

## High Risk License Check

✅ No high-risk licenses (GPL/AGPL/SSPL/BSL) found.

所有依赖均使用宽松许可证（MIT/BSD/Apache-2.0）或弱 copyleft 许可证（MPL-2.0），未发现 GPL/AGPL/SSPL/BSL 等强 copyleft 或源头封锁许可证。Whale Code 可安全开源。

## Notes

- `github.com/sahilm/fuzzy` 与 `github.com/mattn/go-localereader` 在 `vendor/` 中随源码分发，但上游仓库未附带 LICENSE 文件；两者均为 MIT 许可证（见各自 README/源码声明），此处按上游声明的许可证记录。
- `modernc.org/mathutil` 的 LICENSE 文件命名不在 go-licenses 自动识别的正则范围内，但该包属于 modernc.org 生态（BSD-3-Clause）。
- 后续在可联网环境运行 `bash scripts/license-audit.sh` 可自动重新生成完整清单（含 go-licenses 自动识别的许可证文本与源 URL）。
