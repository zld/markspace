# medpipe

可复用的本地媒体处理内核与终端样例程序。输入是项目生成的、带丢包/乱序/重复/损坏的 RTP/RTCP 包流；输出是分段 MP4、静音区间、去重引用、原子检查点和 SHA-256 校验清单。

- Go 1.23（本机已用更高版 Go 验证，`go.mod` 固定 `go 1.23`）
- `github.com/pion/rtp v1.8.11`
- `github.com/pion/rtcp v1.2.15`
- `github.com/abema/go-mp4 v1.29.0` 需求固定在 `go.mod`；公共模块代理当前不存在该 tag，因此通过 `replace` 使用可解析的最新兼容版 `v1.7.4`。在有内部 `v1.29.0` 的环境可删除该 replace。
- 仅依赖 Go 标准库测试工具，无外部服务。

## 构建与测试

```bash
go mod tidy
go build ./...
go test ./...

./scripts/build.sh           # 生成 bin/medpipe 与 bin/genfixture
./scripts/gen-fixtures.sh    # 重新生成 testdata/fixtures
```

macOS 可直接运行 `scripts/` 下的 bash 脚本。

## 验收样例

```bash
go run ./cmd/medpipe demo \
  --seed 17 --packets 12000 \
  --loss 0.02 --reorder 0.08 --duplicate 0.01 \
  --queue-bytes 1048576 --consumer-delay 2ms \
  --checkpoint-every 500 --corrupt-offset 4097
```

终端输出一个 JSON 对象，包含：

- `segments`：片段、时间边界、指纹、MP4 SHA-256、重复引用；
- `silence_ranges`：使用 `start_ns` / `end_ns` / `duration_ns` 的统一纳秒时间；
- `duplicates`、`missing`；
- `queue_peak_bytes`、`queue_limit_bytes`；
- `manifest_sha256`、`output_sha256`；
- 损坏路径额外输出 `corrupt_packet_seq`、`corrupt_byte_offset`、`error_code`。

相同 seed 和参数重跑会得到相同的 `manifest_sha256`。队列峰值由硬字节预算约束，因此不会超过 `--queue-bytes`。

恢复命令：

```bash
go run ./cmd/medpipe demo \
  --seed 17 --packets 12000 --resume --checkpoint-every 500
```

输出 `resume_ms`、`resume_from_seq`、`resume_from_ext_seq` 和 `output_sha256`。恢复前会校验输入 SHA-256 与检查点中的解析器主版本；不匹配时拒绝续跑，并在输出目录写 `resume-rejected.json` 保留原因。

默认输出目录是 `./medpipe-output/`，可通过 `--out` 修改。生成的 `source.mdp` 已存在时不会被覆盖。

## 退出码

构建后的 `bin/medpipe` 精确保留：

- `0`：完成且校验通过；
- `2`：发现已定位的输入损坏，但可恢复部分已完成；
- `1`：参数、解析器主版本、检查点或恢复校验失败。

Go 工具的 `go run` 会把程序的非零退出统一包装成 `go run` 自身的退出码 `1`，并打印 `exit status 2`；程序 JSON 中的 `exit_code` 和直接运行 `bin/medpipe` 的 shell 退出码仍是上述约定。

## 架构

```
sample generator
  -> length-delimited source.mdp
  -> stream.Pipe (byte budget + backpressure)
  -> ingest.RTP/RTCP + ReorderBuffer
  -> clock.Timeline (SSRC, u16/u32 wrap, RTCP correction)
  -> media.Segmenter / SilenceDetector / Fingerprint+Deduper
  -> adapter registry -> mp4v1/go-mp4
  -> manifest.json + checksums.sha256 + atomic checkpoint.json
```

关键包：

- `internal/clock`：统一时间基准、可序列化 `clock.Error` 位置、原子检查点；
- `internal/ingest`：Pion RTP/RTCP 解析、载荷 CRC、扩展序号和重排；
- `internal/media`：切段、静音、帧/片段指纹和重复引用；
- `internal/stream`：硬字节预算队列与背压；
- `internal/adapter`：主版本化解析器注册表；
- `internal/adapter/mp4v1`：主版本 1 的 go-mp4 封装；
- `internal/pipeline`：跨子系统编排、检查点恢复和清单；
- `internal/sample`：确定性样例生成器与本地源容器。

## 边界测试

`go test ./...` 包含四类必需的自生成样例：

1. `TestTimelineReordersAcrossTimestampWrap`：乱序跨越 uint32 时间戳回绕；
2. `TestMissingRTCPReportIsLocatedFallback`：缺失 RTCP 报告时回退接收时间并产生可定位错误；
3. `TestTruncatedPacketAcrossSegmentBoundaryRecoversConsistently`：截断包跨切段边界，并比较中断/恢复与一次跑完的清单字节和 SHA-256；
4. `TestSlowConsumerBackpressureCheckpointRecoveryConsistency`：慢消费者触发背压，验证停止消费后占用不超上限，并验证检查点恢复后的片段、指纹和校验和一致。

另有静音短区间抑制、指纹不匹配不合并、未知解析器主版本、非法 MP4 长度和缺失字段等回归测试。

## 确定性与边界

- 清单不包含墙钟时间和调度相关的背压计数；这些运行时指标只出现在终端结果中。
- RTP 时间戳按 48 kHz 媒体时钟映射为纳秒；帧周期为 20 ms。
- 损坏载荷使用 CRC 分类；演示命令的 `corrupt_byte_offset` 由生成器给出精确注入位置，内核错误位置至少定位到源 chunk/RTP 包起点，误差不超过一个固定尺寸包（192 字节）。
- 内存由 `--queue-bytes` 和 `ingest.DefaultMaxPacketBytes` 共同约束。
