# replaycheck

本地、无外部服务的赛事回放媒体处理内核。它统一 MP4/MOV 容器与 RTP 时间戳的纳秒基准，校验漂移、回绕、VFR 切点、分片边界、批处理检查点和预算阈值。

## 环境与依赖

- macOS / Terminal
- Go 1.23（本机更新版本也可按 `go.mod` 的 `go 1.23` 指令构建）
- `github.com/abema/go-mp4 v1.2.0`
- `github.com/pion/rtp v1.8.9`

这两个版本真实存在且已固定；它们分别只要求较低的 Go 版本，和 Go 1.23 兼容。除非要主动获取上游补丁，否则不需要把依赖“更正”为最新版；直接运行普通 `go mod tidy` 已验证不会改动固定版本。

## 验证命令

```bash
brew install go
go mod tidy
go build ./...
go test ./...
go run ./cmd/replaycheck --self-test --chunk 4 --index ./tmp/index.json
```

也可以执行：

```bash
./scripts/build.sh
./scripts/selftest.sh
```

`--self-test` 会在 `./tmp/samples` 生成恰好五个样例：

1. `01_normal.mp4`：双轨正常样例，含 0.5 ms 可接受漂移。
2. `02_truncated_header.mp4`：截断 moov/头部，输出非负失败偏移。
3. `03_rtp_wrap.rtpfix`：pion/rtp 生成并解析的 32 位 RTP 时间戳回绕样例，回绕计数为 1。
4. `04_vfr.mp4`：逐 stts/DTS 帧生成的可变帧率样例。
5. `05_chunk_corrupt.rtpfix`：第四个完整包后的块边界损坏包。

## 退出码策略

- `0`：自检中的全部预期检测、恢复和摘要一致性通过；或实际批次在阈值内完成。
- `2`：异常率超过 1% 或预算偏差超过 2%。
- `1`：输入不可读/不可恢复、索引摘要不一致或自检断言失败。

自检故意包含两个“预期内失败样例”，所以终端会显示 `simulated_batch_exit_code=1`；这表示真实批次策略会返回 1，但自检进程在正确定位这些损坏后仍返回 0。`internal/batch` 中有直接测试分别覆盖真实批次的退出码 1 和 2。

## 关键输出

终端包含：

- `files=5`
- `tracks_checked`
- `wrap_count=1`
- `drift_ms`
- `vfr_boundary_error_ms`
- `anomaly_rate`
- `budget_deviation_pct`
- `recovery_recomputed_blocks=1`
- `index_sha256`、`repeat_index_sha256`、`index_digest_match=true`
- 异常字节/纳秒起止偏移
- 每个文件的逐项 `conclusion[...]`

索引是确定性 JSON，不写墙钟时间。返回的 `index_sha256` 等于索引文件实际字节的 SHA-256，可使用 `shasum -a 256 ./tmp/index.json` 核对。

## 模块结构

- `internal/clock`：整数纳秒基准、时基换算、漂移阈值、RTP 32 位回绕展开、统一错误和原始偏移。
- `internal/container`：有界读取、MP4/MOV 头和 box 预检查、go-mp4 探测、track/mdhd/stts 时基与 DTS 帧时间线。
- `internal/events`：事件时间到“其后首个可解码帧”的映射和量化误差。
- `internal/chunks`：分片单调性、重叠/间隙和目标区间覆盖校验。
- `internal/fixture`：确定性 MP4 与长度前缀 RTP 夹具生成/解析。
- `internal/index`：确定性索引、输入摘要和恢复检查点。
- `internal/batch`：离线编排、固定块检查点、恢复、预算统计和自检场景。
- `cmd/replaycheck`：纯终端入口。

## 安全边界

单文件读取上限为 2 MiB，单个 RTP 夹具包上限为 8 KiB。box size、RTP record length 和读取器都有边界检查，损坏输入不会触发无界内存读取。
