# mediad — 可追溯的媒资接入 / 去重 / 归档 / 恢复流水线

纯本地、无外部服务依赖的 Go 项目。把 H.264 / H.265 / AV1 裸码流与关键帧索引处理为
规范化轨道、内容寻址派生物和确定性归档清单，并通过分阶段状态机支持中断恢复。
同一份源资产重复投递或恢复后得到一致结果。

## 环境与安装（macOS / Darwin）

1. 安装 Go 1.23（本机可用更高版本，`go.mod` 声明 `go 1.23.0`）。
2. 在项目目录执行：

   ```sh
   go mod tidy
   go build ./...
   go test ./...
   go run ./cmd/mediad demo --work ./tmp/acceptance --seed 17
   ```

   或一键执行 `./scripts/build.sh`（可用 `WORK` / `SEED` 环境变量覆盖）。

> 依赖版本说明：需求指定 `go-mp4 v1.30.0`，但该标签在 GitHub 与 Go 模块代理上均
> 不存在（截至当前，`github.com/abema/go-mp4` 的最高发布版本为 **v1.7.4**）。
> 因此本项目固定使用实际可获取的 **v1.7.4**；`pion/rtp v1.8.11` 与需求一致。
> 版本固定于 `go.mod`，由 `go mod tidy` 解析。

## 模块与数据流

```
裸码流 + 关键帧索引
   │  ingest: 有界分块读取(64KiB) → 索引逐项校验 → 编解码边界扫描 → SHA-256 指纹/去重
   ▼
normalize: 整数刻度规范化轨道(timescale) → 确定性 MP4(go-mp4 序列化时间字段)
   ▼
   derive: 按“规范轨道摘要 + 处理参数”内容寻址的派生物（质量报告）
   ▼
pipeline: ingest→normalize→derive→manifest→archive 五阶段，产物落盘校验后才提交
   ▼
manifest: 固定字段顺序、按 source_sha256 排序的确定性清单（原子写）
   ▼
archive: 恢复时校验源摘要、阶段记录、派生摘要与清单条目
```

| 包 | 职责 |
|---|---|
| `internal/model` | 统一整数纳秒时间基、有理数换算（无浮点累计）、结构化错误与退出码 |
| `internal/ingest` | 有界缓冲池分块读取、背压/取消、索引校验、Annex-B/AV1 边界扫描、指纹去重 |
| `internal/rtpingest` | 基于 pion/rtp 的 RTP 解析、时间戳换算与三码种解包 |
| `internal/normalize` | 规范轨道（显式 timescale，拒绝零时基）与确定性 MP4 编码 |
| `internal/derive` | 内容寻址派生物存储，源级失效隔离 |
| `internal/manifest` | 确定性、排序、去重、原子写的清单 |
| `internal/pipeline` | 阶段状态机：仅在产物校验后提交，中断清理后续做 |
| `internal/archive` | 恢复校验源/阶段/派生/清单，重复条目检测 |
| `internal/quality` | 码率、PSNR、SSIM 确定性度量 |
| `internal/fixture` | 本地生成成功与损坏样例（不引用外部文件） |
| `internal/demo` | 库层端到端编排；`cmd/mediad` 仅解析参数并调用 |

## 关键不变量

- **源不可变**：以 SHA-256 标识；相同摘要重复投递只复用已验证结果，不重复追加清单。
- **有界内存**：固定 64 KiB 块 + 有界缓冲池（队列深度 4 → 峰值 5×64 KiB），
  内存只随配置增长，不随素材总长增长；满队列阻塞上游，取消即释放 goroutine 与临时文件。
- **严格校验**：索引逐项检查偏移/长度溢出/顺序/关键帧位置；截断或非法头部返回含
  编码与首个字节偏移的结构化错误。
- **时间确定性**：整数刻度 + 显式 timescale，拒绝零时基与不可表示时间；对齐误差 ≤ 1 ns。
- **阶段原子性**：未提交临时产物在重跑时清理；已提交内容摘要不变。
- **派生物隔离**：源变化只失效依赖该源的指纹、清单与派生缓存。

## 演示输出

每行一个 JSON 对象，字段包含 `source_sha256、codec、target_bps、actual_bps、
bitrate_error_pct、psnr_db、ssim、manifest_start_ns、manifest_end_ns、
alignment_error_ns、buffer_peak_bytes、stage、exit_code`；损坏样例额外含
`error_code` 与 `error_offset`，并以退出码 2 结束该样例；末行 `summary` 给出各路径计数，
整体仅在全部预期满足时退出 0。

验收门槛：时间对齐误差 ≤ 1 ns、峰值缓冲不超过配置值、码率偏差 ≤ 5%、
PSNR ≥ 30 dB、SSIM ≥ 0.95、重复运行清单字节一致。
