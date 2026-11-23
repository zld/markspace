# mediqc

广播节目母版与字幕的批量检查、再封装与分片交付工具。纯终端形态，无外部服务依赖。

## 构建与测试

```sh
go mod download
go build ./...
go test ./...
```

## 演示

```sh
go run ./cmd/mediqc demo --seed 42 --resume-check --slow-sink-ms 20
```

输出字段：`parse_roundtrip`、`keyframe_hit`、`segment_hashes`、
`resume_manifest_equal`、`peak_buffer_bytes`、`corrupted_rejected`、
`cache_tamper_recomputed` 等。退出码：0 全部通过，1 输入或处理失败，2 参数错误。

参数：

- `--seed`：合成素材的确定性种子（默认 42）
- `--resume-check`：模拟中断并从检查点恢复，比较清单一致性
- `--slow-sink-ms`：模拟慢速下游的每块延迟（触发背压）
- `--max-buffer-bytes`：流水线缓冲上限（默认 256 KiB）

## 架构

| 模块 | 职责 |
| --- | --- |
| `internal/clock` | 有理数时间基准；24/25/29.97 DF/NDF/50 fps 时间码换算，往返误差 ≤1 帧 |
| `internal/fault` | 跨模块错误码、偏移与恢复建议 |
| `internal/subtitle` | SRT/WebVTT 解析与序列化，统一 90 kHz 时间轴；拒绝负时长、非法时间码、跨日界字幕 |
| `internal/segment` | 关键帧分片规划、多码率切点对齐（≤1 帧）、MP4 stss/trun 关键帧扫描（go-mp4） |
| `internal/cache` | 内容寻址缓存（输入字节+时间基准+参数）；命中仍校验来源与摘要，篡改即隔离重算 |
| `internal/checkpoint` | 分片摘要持久化后才推进检查点；恢复不重复提交 |
| `internal/stream` | 有界字节队列、背压、慢消费者处理；RTP 封装（pion/rtp） |
| `internal/manifest` | 确定性清单输出，摘要比较采用固定长度安全比较 |

数据流：字幕解析与时间基准校正 → 分片规划和多码率对齐 → 流式输出及摘要校验；
缓存与检查点贯穿各阶段，所有模块共用时间基准和错误模型。
