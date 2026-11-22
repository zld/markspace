# mediadist — 确定性跨节点媒体分发内核

把多个边缘节点收到的 DASH/HLS/RTP 直播流整理成跨节点可分发、**可核验**的
媒体分片清单。纯本地运行，不访问任何在线源，样例数据全部在进程内确定性生成。

## 技术栈与依赖

- Go 1.23（本地以 Go 1.27 工具链验证，`go.mod` 声明 `go 1.23`）
- `github.com/pion/rtp v1.8.11`：RTP 包头解析
- `github.com/abema/go-mp4`：fMP4（`ftyp/moof/mdat`）分片写出与回读校验
  - 需求中给出的 `v1.28.0` 在上游不存在（该模块最高发布版本为 `v1.7.4`），
    因此固定使用最新可用的 `v1.7.4`，见 `go.mod`
- 其余仅用 Go 标准库（`testing`、`math/big`、`crypto/sha256` 等）

## 构建与测试

```bash
go mod tidy
go build ./...
go test ./...
```

## 演示

```bash
# 常规场景：2% 丢包、180ms 迟到、3 码率
go run ./cmd/mediadist demo --loss 0.02 --late-ms 180 --rates 3

# 注入零 timescale → error_code=INVALID_TIMESCALE，退出码 2
go run ./cmd/mediadist demo --corrupt-timescale 0

# 迟到超出重放窗口 → error_code=REPLAY_WINDOW_EXCEEDED，退出码 3
go run ./cmd/mediadist demo --late-ms 9000 --replay-window-ms 1000
```

> 关于退出码：`go run` 在子进程非零退出时自身固定返回 1（并打印
> `exit status N`），这是 Go 工具链行为，与本程序无关。编译后的二进制
> 会原样返回 2/3。需要在终端脚本中拿到真实退出码时使用：
>
> ```bash
> ./scripts/demo.sh demo --corrupt-timescale 0   # 退出码 2
> ./scripts/demo.sh demo --late-ms 9000 --replay-window-ms 1000  # 退出码 3
> ```

常规演示输出（确定性，每次相同）：

```
sync_error_ms=8.0 loss_recovery_rate=0.95 p95_latency_ms=420
aligned_representations=3 manifest_sha256=<64 位十六进制> ...
```

## 架构与数据流

```
manifest 解析器 ──DASH/HLS 有理数时间戳──┐
RTP 接收器(重排+恢复) ──事件──────────────┼──> timeline 引擎(水位/有界回溯)
media 元数据归一(共享错误模型) ──片段结果──┘            │
                                                       ▼
                              aligner 跨码率对齐 → 分片清单 + 指标 + sha256
```

| 文件 | 职责 |
| --- | --- |
| `timeline/clock.go` | 有理数媒体时基（`math/big` 防溢出）、共享错误模型 |
| `timeline/watermark.go` | 事件水位、迟到数据有界回溯、确定性排序与摘要 |
| `timeline/replay.go` | 回溯深度上限、快照恢复、顺序无关重算 |
| `manifest/dash.go` | MPD 表示层/分片解析，timescale→毫秒有理数换算 |
| `manifest/hls.go` | discontinuity epoch、媒体序号保留、跨 epoch 拼接拦截 |
| `manifest/align.go` | 跨码率起止边界对齐，超阈值拒绝发布并报告 epoch/rep/序号 |
| `transport/rtp.go` | RTP 包校验、16/32 位回绕距离、事件时间换算 |
| `transport/recovery.go` | 有界抖动缓冲：乱序/重复/缺包/回绕，超窗只标记不伪造 |
| `media/metadata.go` | 声道布局与采样率精确归一，不可映射即报错 |
| `media/color.go` | 色彩冲突检测、来源值保留、固定优先级选取 |
| `media/fragment.go` | go-mp4 fMP4 分片写出与结构回读校验 |
| `pipeline/*.go` | 端到端装配、夹具生成、跨阶段不变量、指标与摘要 |
| `cmd/mediadist/main.go` | 终端 `demo` 命令与编码错误/退出码 |

## 关键设计

- **确定性**：所有随机行为由 `Config.Seed` 驱动；引擎按
  `(epoch, time, seq, id)` 规范排序，输入到达顺序不影响最终摘要，重复运行
  `manifest_sha256` 一致。
- **有界内存**：清单行数、表示层数、分片数、RTP 包大小/数量、分片采样数均有
  上限，畸形输入返回编码错误而非无界增长。
- **不伪造媒体**：超出重排/重放窗口的缺口只标记为 unrecoverable 并计入指标。
- **共享错误模型**：所有子系统通过 `timeline.Error{Code,...}` 返回稳定机器码，
  如 `INVALID_TIMESCALE`、`REPLAY_WINDOW_EXCEEDED`、`ALIGNMENT_REJECTED`、
  `UNSUPPORTED_CHANNEL_LAYOUT`、`CROSS_EPOCH_STITCH`。
- **跨阶段不变量**：时间单调、分片不重叠、`Recovered <= Missing`、边界偏差不
  超阈值；失败后可从最近水位快照重放并得到相同清单摘要。
