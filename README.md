# mediapack

一个纯本地、可复用的多机位媒体处理内核示例：生成双码率 MP4/RTP/字幕/章节/ID3/emsg/SEI 夹具，在统一纳秒时间线上完成回绕扩展、跨轨对齐、拓扑调度、有界队列、丢包恢复、关键帧分片、清单摘要与中断续接。

## 验收命令

在项目根目录执行：

```bash
brew install go
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediapack demo --seed 17 --bitrates 2 --queue-capacity 8 --report json
go run ./cmd/mediapack demo --seed 17 --corrupt truncate --report json
go run ./cmd/mediapack demo --seed 17 --loss 0,3,4 --reorder 2 --report json
go run ./cmd/mediapack demo --seed 17 --resume --report json
```

也可运行完整脚本：

```bash
./scripts/acceptance.sh
```

默认输出目录按场景隔离：

- 普通/续接：`output/demo/`
- 截断损坏：`output/corrupt-truncate/`
- 丢包/乱序：`output/rtp-recovery/`

可用 `--output <dir>` 覆盖。所有分片、清单和 JSON 产物只能写入该目录；`../` 等路径穿越会被拒绝。

## 退出码

- `0`：正常，或损坏可恢复且已在安全前缀/跳过坏单元后续接（JSON 中带 `warnings`）。
- `2`：命令行或参数错误。
- `3`：清单或分片摘要不匹配，拒绝不安全续接。
- `1`：其他无法安全继续的输入或 IO 错误；例如 `--corrupt invalid-length` 会报告偏移和 `invalid_length` 类别。

## 关键保证

- 时间基为有理数 `numerator/denominator`，项目内部统一转换为纳秒。
- 固定宽度时间戳处理回绕、窗口内乱序和重复；无法判定的远距离位移返回带 `track`、`source`、偏移和类别的 `model.Error`。
- 轨道图拒绝缺失依赖和环，拓扑顺序按 ID 稳定排序。
- 字幕和定时元数据依赖的视频关键帧边界未就绪时，`KeyframeGate` 阻止封段。
- `stream.BoundedQueue` 容量固定；队列峰值不会超过 `--queue-capacity`，满队列超时返回可恢复的 `queue_backpressure`。
- RTP 在重排窗口内恢复顺序；窗口外缺口记录序列范围和未恢复包数，重复包不重复计数。
- MP4 夹具覆盖正常盒、截断盒、非法长度、CRC 坏单元和缺失关键帧。CRC/长度完整的坏 sample 可跳过；截断输入保留已校验前缀。
- 分片边界只规划一次，两个码率表示层使用完全相同的起止时间；目标边界没有关键帧时延后到下一关键帧。
- `manifest.json` 包含边界、轨道、事件、分片路径、大小和 SHA-256。清单摘要排除自身摘要字段，因此重复输入产生相同摘要。
- `--resume` 先校验已有清单自摘要和所有已存在分片摘要；缺失分片重新生成，已存在但摘要不符则退出码 `3`。

## 主要包

| 路径 | 职责 |
| --- | --- |
| `model/` | 共享轨道 ID、错误类别、媒体单元和警告模型 |
| `timeline/time.go` | 有理数时间基、纳秒转换、回绕/乱序/重复判定 |
| `timeline/graph.go` | 轨道依赖图、环和缺失依赖拒绝、稳定拓扑序 |
| `timeline/align.go` | 音画对齐、事件去重、关键帧封段门 |
| `ingest/rtp.go` | 基于 `github.com/pion/rtp` 的序号重排和缺口统计 |
| `ingest/metadata.go` | SUB/CHAP/ID3/EMSG/SEI 夹具解析 |
| `ingest/mp4.go` | 基于 `github.com/abema/go-mp4` 的盒检查和 sample 盒校验 |
| `stream/queue.go` | 固定容量队列、背压超时、峰值统计 |
| `stream/recover.go` | 丢包、跳过坏单元和截断前缀恢复状态 |
| `segment/boundary.go` | 共同时间线和关键帧边界规划 |
| `segment/manifest.go` | 清单、SHA-256、安全路径和续接校验 |
| `pipeline/run.go` | 生成样例并串联全部阶段 |
| `report/metrics.go` | P95、同步误差、恢复率、队列峰值和分片统计 |
| `sample/` | 确定性双码率、多轨元数据和 RTP 夹具生成 |
| `cmd/mediapack/` | 终端入口 |

## 测试覆盖

```bash
go test ./...
```

包含时间线回绕/歧义、轨道图环和稳定拓扑、队列背压与峰值、RTP 乱序/缺口/重复/回绕、MP4 损坏类别、边界一致性、清单防篡改和端到端续接。
