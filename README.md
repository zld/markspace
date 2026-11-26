# 多机位 PCM/RTP 采集核对与可复核分片

纯本地 Go 实现，无外部服务。源码、单测、夹具生成逻辑和验证脚本均在仓库内；运行产物写入 `--out` 指定目录。

## 快速验证

```bash
go mod tidy && go mod download
go build ./...
go test ./...
go run ./cmd/acceptance --case all --out "$TMPDIR/media-check"
```

也可运行：

```bash
./scripts/verify.sh "$TMPDIR/media-check"
```

脚本会执行模块整理、构建、全量测试、两次验收运行，并用编译后的二进制验证退出码。

## 命令与退出码

```bash
go run ./cmd/acceptance --case all|normal|damage|recovery|repeat --out DIR
go run ./cmd/acceptance --input /path/to/foreign.wav
```

- `0`：所有预期路径通过（包括验收夹具中“被正确识别”的损坏样例）。
- `2`：命令参数错误。
- `3`：外来 WAV/输入损坏。

注意：`go run` 会把被运行程序的非零退出统一包装为 `1`；需要核验原始 `2/3` 时，请先 `go build -o ... ./cmd/acceptance` 再运行二进制，`scripts/verify.sh` 已这样处理。

版本说明：固定的 `github.com/pion/rtp v1.10.6` 自身声明 `go 1.24.0`，因此 `go mod tidy` 会把主 `go.mod` 的 go 指令维护为 `1.24.0`。在 Go 1.23.12 上使用默认 `GOTOOLCHAIN=auto` 时可自动获取兼容工具链；若强制 `GOTOOLCHAIN=local` 且离线，则题目指定的该依赖版本与 Go 1.23.12 存在工具链下限冲突。

## 输出字段

每个用例向 stdout 输出一行稳定 JSON，包含：

`target_bps`、`actual_bps`、`delta_pct`、`snr_db`、`align_err_samples`、
`bad_offset`、`gap_count`、`source_sha`、`invalidated`、`cache_hit`。

正常/恢复样例满足码率偏差 ≤ 0.1%、SNR ≥ 90 dB、对齐误差 ≤ 1 采样。损坏样例报告首错字节偏移、轨道/采样时间区间和缺口，并且不发布跨越缺口或来自坏容器的完整分片。

## 包布局

- `internal/timebase`：有理采样/滴答换算、溢出检查、稳定错误码与时间区间。
- `internal/wav`：流式 RIFF/WAVE PCM 16/24/32 校验和帧解码。
- `internal/asset`：SHA-256 不可变源存储。
- `internal/track`：规范稀疏轨道、声道映射和分块归一化。
- `internal/rtp`：pion/rtp 解析、16 位序号回绕、去重和重排窗口。
- `internal/gap`：突发丢失与窗口外迟到缺口台账。
- `internal/budget`：带宽滑窗和 80% 预算档位选择。
- `internal/validate`：源、轨道、缺口、分片时间戳/长度交叉复检。
- `internal/derive`：go-mp4 fMP4 分片写入、结构读取和原子派生。
- `internal/cache`：源摘要+配置键、依赖失效图和内容缓存。
- `internal/manifest`：稳定 JSON 清单与原子落盘。
- `internal/metrics`：码率、SNR 和采样对齐。
- `cmd/acceptance`：自生成夹具、四类验收用例和终端 JSON。

## 设计约束

- WAV 按小帧流式读取，重排窗口和归一化块大小有界。
- 缺口只记录真实丢失/迟到区间，不用零样本补造。
- 源对象按 SHA-256 寻址；缓存键包含源摘要与配置摘要。
- 派生物使用同目录临时文件、`fsync`、`rename` 原子发布。
- 清单不含墙钟时间，重复运行字节和摘要稳定。
