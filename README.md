# medpipe

可复用的本地 Go 媒体处理库，把异地录音棚提交的多轨 PCM/WAV 采样块汇聚为
可核验、可复算的统一音频段与清单。纯本地运行，不依赖外部媒体程序或网络服务。

## 子系统

| 包 | 职责 |
|---|---|
| `timeline` | 整数采样刻度时钟、时间换算（禁止浮点累积漂移）、轨道区间校验、依赖图拓扑排序与环路检测、共享错误类型 |
| `audio` | PCM 16/24/32 位 WAV 解析、跨块保留滤波状态的流式重采样、显式矩阵声道布局归一、BS.1770 风格响度与 -1 dBTP 真峰值限幅 |
| `stream` | 容量可配置、硬上限 64 块的有界队列与生产端背压；runner 调度消费者、上下文取消与检查点；pion/rtp 序号与时间戳校验 |
| `manifest` | 时间区间与处理统计记录、跨阶段不变量校验、确定性 sha256、go-mp4 ISO BMFF 清单载体（ftyp+mdat，可解析回读） |
| `fixture` | 带种子的确定性样例生成器与五类故障注入，不引用任何外部素材 |
| `app` / `cmd/medpipe` | CLI 适配器，只负责生成样例、驱动库、打印终端摘要 |

数据流：WAV 块 → 时间线与轨道图 → 音频流水线 → 有界队列 → 清单。

## 构建与测试

```sh
go mod tidy
go build ./...
go test -race ./...
```

## 演示

```sh
go run ./cmd/medpipe demo --seed 17 --rate 48000 --channels 6 \
  --queue 4 --slow-ms 2 --out /tmp/medpipe-demo
```

输出字段：`target_bitrate_bps`、`actual_bitrate_bps`、`bitrate_delta_pct`
（|δ| ≤ 2.0）、`input_lufs`、`output_lufs`、`true_peak_dbtp`（≤ -1.0）、
`quality_clipped_samples`、`manifest_alignment_ms`（|δ| ≤ 1.0）、
`peak_queue_blocks`（≤ 队列容量）、`output_sha256`。

故障演示：

```sh
go run ./cmd/medpipe demo --fixture truncated --out /tmp/medpipe-bad
```

打印 `error_stage`、`track_id`、`recovery_checkpoint`。退出码语义：`0` 成功、
`2` 可定位的处理故障、`1` 内部错误。注意 Go 1.23 的 `go run` 透传子进程退出码；
在更新的工具链上 `go run` 会把非零码包装为 1，此时使用 `scripts/medpipe.sh`
（构建真实二进制并原样转发退出码）。

## 故障夹具

`truncated`（WAV 截断）、`missing`（上游缺块，下游失败并保留检查点）、
`misaligned`（时间戳倒退/错位）、`seqjump`（RTP 序号跳变）、
`badlength`（非法块长度）。截断、缺块与时间错位均传播至清单阶段的 failure 记录。

## 关键设计

- 所有区间使用 `[Start, End)` 整数半开区间；时间换算用整数除法与固定舍入策略。
- 重采样位置以整数输入索引 + 有理相位跟踪，末块固定零填充，输出帧数与理论值
  误差不超过一帧；分块与一次性处理帧数一致。
- 未知声道布局直接拒绝；矩阵保证每个输入声道至少有一个非零增益，不静默丢弃。
- 检查点单调提交，回归被拒绝；相同参数重复运行的 `output_sha256` 一致。
- 队列内存上界为 `capacity + 2`（容量块 + 至多一个在推、一个在弹）。
