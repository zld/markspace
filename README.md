# mediacore

本地、无外部服务的多码率媒体时基对齐、随机访问索引、区间读取与可恢复管线。

## 环境与依赖

- macOS / Darwin
- Go 1.23+（`go.mod` 固定 `go 1.23`）
- 依赖固定：
  - `github.com/abema/go-mp4 v1.3.0`
  - `github.com/pion/rtp v1.8.11`

安装 Go 模块依赖：

```bash
go mod download
```

说明：`go-mp4` 与 `pion/rtp` 是 Go module，不是 Homebrew formula；在当前 Homebrew 中执行 `brew install go-mp4 pion/rtp` 会报无此 formula。若本机没有 Go，可先 `brew install go`。

## 常用命令

```bash
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediacore demo --work ./tmp/demo
go run ./cmd/mediacore verify --work ./tmp/demo
```

或运行完整验收脚本：

```bash
./scripts/acceptance.sh
```

脚本会执行 tidy、build、全部测试、demo、verify，并用编译出的二进制核对精确退出码。

## 退出码

- `0`：默认 `verify` 的全部验收通过，包括“预期内”的损坏和输入变化用例。
- `2`：直接运行损坏/非法清单用例，例如：

  ```bash
  go build -o ./tmp/bin/mediacore ./cmd/mediacore
  ./tmp/bin/mediacore verify --work ./tmp/demo --case truncated-shard
  ./tmp/bin/mediacore verify --work ./tmp/demo --case illegal-sample
  ./tmp/bin/mediacore verify --work ./tmp/demo --case zero-timescale
  ./tmp/bin/mediacore verify --work ./tmp/demo --case time-regression
  ```

- `3`：输入摘要变化导致旧检查点被拒绝：

  ```bash
  ./tmp/bin/mediacore verify --work ./tmp/demo --case changed-input
  ```

注意：`go run` 工具本身会把子进程的非零退出码统一为 `1`，同时打印 `exit status 2` 或 `exit status 3`；要在 shell 中直接获得精确的 2/3，请使用 `go build` 产物。

## 输出

每个工作目录包含：

- `fixtures/`：自生成 DASH、HLS、初始化段、fMP4/MP4 与损坏夹具。
- `indexes/<stable-key>.json`：随机访问样本索引。
- `provenance/<stable-key>.json`：输入哈希、映射规则版本、索引摘要和来源映射。
- `reports/<stable-key>.json`：区间查询与边界偏差。
- `checkpoint.json`：输入摘要、配置摘要和已完成稳定条目。
- `first-run.json`、`verify-report.json`、`bundle.json`：验收记录。

正常样例的 tick 与 byte 首尾偏差均为 0；超过 20 ms 的 DASH 表示层会标记为 `ALIGNMENT_FAIL`，区间查询拒绝静默混用。

## 架构

- `internal/clock`：统一刻度、整数有理数换算、溢出/零分母错误、20 ms 对齐阈值。
- `internal/diagnostic`：跨子系统共享的清单、表示层、条目、序号和字节位置错误模型。
- `internal/manifest`：DASH/MPD 与 HLS/m3u8 解析，输出统一 `Entry`。
- `internal/index`：go-mp4 解析 mdhd、stts/stsz/stco/stss、tfdt/trun；流式哈希、索引和半开区间查询。
- `internal/rtpx`：pion/rtp 时间戳 32 位扩展与包序核验。
- `internal/provenance`：来源、摘要和原子 JSON 持久化。
- `internal/checkpoint`：输入/配置摘要绑定的原子检查点。
- `internal/pipe`：有界队列、缓冲字节预算、背压、取消和提交/恢复。
- `internal/fixture`：正常、截断、非法样本、零 timescale、时间倒退等本地夹具。
- `internal/app`：可复用 demo/verify 工作流；`cmd/mediacore` 只是薄终端入口。

所有媒体引用都会解析并限制在 `--work` 目录内，处理过程不访问网络或外部素材。
