# mediaqc — 本地媒体验收与再封装内核

一套纯本地、无外部服务依赖的 Go 媒体处理内核，用于批量验收外部制作团队交付的
HLS/DASH/RTP/fMP4 节目，完成抖动缓冲、丢包恢复、音画同步、批量诊断与再封装，
并为每个素材输出可复核的量化证据。

## 运行环境与依赖

- Go 1.23（`go.mod` 固定 `go 1.23`）
- `github.com/abema/go-mp4 v1.4.1`、`github.com/pion/rtp v1.8.11`（版本固定）
- 仅标准库 `testing`，无网络访问、无项目目录外写入、无密钥或连接串

## 一键验收

```bash
go mod tidy && go build ./... && go test ./... && \
  go run ./cmd/mediaqc -selftest -seed 17 -window 8 -tolerance-ms 20
```

终端会打印三条路径（成功 / 损坏 / 恢复）与量化指标：

| 指标 | 含义 | 验收基线 |
| --- | --- | --- |
| `av_sync_error_ms` | 共同时间轴上的最大音画漂移 | 正常绝对值 ≤ 容差（默认 20ms） |
| `packet_loss_recovery_rate` | 单丢包场景的 XOR 恢复率 | 单丢 = 1.000，双丢 = 0.000 |
| `p95_latency_ms` | 1000 个确定性样本的 p95 延迟 | 有限且 > 0 |
| `unrecoverable_gaps` | 终止时仍未恢复的缺口数 | 双丢/截断场景 > 0 |
| `manifest_sha256` | 输出清单内容哈希 | 同 seed 两次运行完全一致 |
| `invariant_failures` | 跨阶段不变量违规数 | 0 |

连续两次运行，除耗时字段外摘要与所有哈希必须逐字节相同。

### 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 所有路径与不变量通过 |
| 1 | 存在不可恢复素材错误，但其他素材完成（本自检的预期码） |
| 2 | 参数或全局配置无效（如 `-window 0`、未知 flag） |
| 3 | 不变量或重复运行确定性校验失败 |

## 架构与数据流

```
HLS/DASH 适配器 ─┐
MP4/RTP 适配器 ─┼─> 统一 model.Event（绑定整数有理数 clock.Timebase）
版本注册表     ─┘            │
                             v
            有界抖动缓冲（乱序重排/去重/超窗判定）
                             v
            单 XOR FEC 恢复（仅填补已声明缺口）
                             v
            音画同步对齐（漂移容差）+ 音频/色彩归一策略
                             v
            批处理管线（阶段检查点 + 跨阶段不变量）
                             v
            再封装清单（确定性 HLS + sha256）与指标
```

## 包结构

- `internal/clock` — 整数有理数媒体时钟、跨时基精确换算、16/32 位回绕
- `internal/model` — 分层错误码（含可恢复性、素材/轨道/阶段坐标）与统一事件
- `internal/adapter` — 注册表/能力协商/解析上限；`hls`、`dash`、`mp4`、`rtp` 子包
- `internal/buffer` — 有界抖动缓冲
- `internal/recovery` — 单 XOR 冗余 FEC
- `internal/sync` — 共同时间轴、漂移测量与单调校验
- `internal/normalize` — 声道布局/采样率策略、色彩元数据优先级与隔离
- `internal/pipeline` — 分阶段批处理、检查点、跨素材隔离、不变量
- `internal/packager` — 确定性清单与哈希
- `internal/fixture` — seed 驱动的故障夹具生成（八类场景）
- `internal/metrics` — 百分位、恢复率等量化证据
- `internal/runner` / `cmd/mediaqc` — 终端编排与入口

## 故障夹具（本地生成，不引用外部文件）

乱序/重复 RTP、单包丢失、双包丢失、截断 MP4 分片、零/非整数 timescale、
缺失清单引用、色彩元数据冲突。双包丢失、截断分片与元数据冲突会跨子系统传播。

## 安全边界

解析尺寸、分片数、嵌套深度、轨道数与缓冲窗口均有硬上限（见
`adapter.DefaultLimits`）；未知色彩描述只隔离不伪装默认值；声道转换必须有
显式下混映射，绝不静默丢声道。
