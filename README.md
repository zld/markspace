# mediad — 可复用 Go 媒体处理内核

区域直播分发跨节点传输场景下的统一媒体处理内核：统一音频转换规则，在有界内存内
维持音画对齐，并保证迟到数据可重算、结果可复现。纯本地、无外部服务依赖。

## 运行环境与依赖

- Go 1.23（在更新工具链上同样可构建，`go.mod` 声明 `go 1.23`），目标平台 macOS。
- 第三方依赖通过 Go Modules 固定版本：
  - `github.com/abema/go-mp4 v1.3.0` — 读取 MP4 轨道元数据（自定义 `clap` box 由本库注册）。
  - `github.com/pion/rtp v1.8.11` — RTP 包头解析与序号处理。
- MXF 元数据解析、有理数时钟、音频处理、时间线、有界缓冲与指标全部只用标准库实现。

## 模块与数据流

```
自生成 MXF/MP4 元数据 ─► meta(解析 + MXF>MP4>缺省 裁决) ─┐
自生成 RTP 包 ─► rtp(序号/时间戳分类、窗口内恢复) ────────┤
                                                          ├─► clock(共享纳秒有理数时钟)
PCM 音频 ─► audio(声道归一 → 重采样 → 响度) ───────────────┤
事件流 ─► timeline(水位、迟到窗口、确定性回溯) ────────────┤
                                                          └─► stream(有界背压) ─► report(指标 JSON)
```

| 包 | 职责 |
| --- | --- |
| `internal/model` | 统一结构化错误 `MediaError`（错误码 + 轨道号 + 字节偏移）。 |
| `internal/clock` | 有理数媒体时钟，统一转纳秒基准并保留输入刻度；溢出/非单调 DTS/跨轨基准不一致均报错，不静默钳制。 |
| `internal/meta` | MXF（标准库 KLV/Local Set）与 MP4（go-mp4）解析、旋转/裁剪/色彩冲突裁决与来源标注。 |
| `internal/audio` | 声道布局归一（固定下混系数）、有理线性插值重采样（固定舍入/边界钳制）、RMS 响度归一与真峰限幅。 |
| `internal/rtp` | 16 位回绕安全的序号分类（乱序/重复/丢失），恢复窗口内重排、窗口外计不可恢复。 |
| `internal/timeline` | 事件时间水位，窗口内迟到触发整区间确定性重算，窗口外 `LATE_REJECTED`；同事件集任意到达顺序哈希一致。 |
| `internal/stream` | 字节数与队列长度双硬上限的有界缓冲；慢消费者阻塞背压，取消/关闭/满上限释放等待者。 |
| `internal/report` | 最近秩 P95、同步误差、恢复率与验收 JSON 汇总。 |
| `cmd/mediad` | 本地验收适配入口，仅负责编排与输出，核心能力都在 `internal/`。 |

## 确定性保证

- 所有舍入采用固定的“半值远离零”规则，中间乘积经 `math/big` 或纯整数运算，无浮点求和顺序依赖。
- demo 使用固定种子的 LCG（`cmd/mediad/rng.go`），不调用全局随机源；时间戳为种子派生值而非墙钟。
- 有界缓冲的阻塞时长按等待次数以固定 tick 记账，跨运行逐字节一致。
- 时间线与音频输出均带 FNV-1a 哈希，重复运行必须得到相同哈希。

## 验收

在项目目录的 macOS Terminal 执行：

```bash
go mod tidy && go build ./... && go test ./...
go run ./cmd/mediad demo --seed 42 --late-window-ms 120 --buffer-bytes 65536 --consumer-delay-ms 8
```

或一键门禁（含 `-race` 并发检测）：

```bash
scripts/accept.sh
```

退出码 0 表示全部断言通过。输出 JSON 至少包含 `sync_error_ms`、`packet_recovery_rate`、
`p95_latency_ms`、`buffer_peak_bytes`、`late_recomputed`、`late_rejected`、
`metadata_conflicts`、`error_codes`、`timeline_hash`。demo 自生成有效及损坏的
MXF/MP4、乱序与丢失 RTP、窗口内外迟到事件和慢消费者；损坏输入打印错误码并继续处理独立轨道。

## 测试覆盖

`go test ./...` 覆盖解析、音频、传输、时间线与背压五个子系统共 50+ 用例，
`cmd/mediad` 另含端到端可复现性与断言测试。
