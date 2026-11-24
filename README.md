# medpipe

可复用的本地 Go 媒体处理内核：RTP 重排去重、MPEG-TS/PSI 校验、33 位时钟展开、显式轨道映射、无损 MP4 转封装，以及带 WAL 的原子提交/恢复/回滚。

所有样例均由 Go 测试和 `samplegen` 确定性生成，不依赖外部媒体文件或网络服务。

## 环境

- macOS (Darwin)
- Go 1.23.x
- `github.com/abema/go-mp4 v1.4.1`
- `github.com/pion/rtp v1.8.11`

安装并固定 Go 1.23：

```bash
brew install go@1.23
export PATH="/opt/homebrew/opt/go@1.23/bin:$PATH"
```

## 验收命令

```bash
go mod tidy
go build ./...
go test ./...
go run ./cmd/medpipe demo --seed 17 \
  --faults duplicate,reorder,burst-loss,cc-jump,psi-version,truncate \
  --resume
```

命令会在 `medpipe-output/` 下生成：

- `normal/`：重复、乱序、突发丢包经冗余恢复后得到的正常样例和 MP4；
- `resume/`：WAL 已 prepared、提交前中断，再由 `--resume` 原子恢复的样例；
- `corrupt/`：CC 跳号、PSI 坏 CRC/版本切换和截断样例；只保留输入与 WAL，不生成 MP4 或成功提交标记。

stdout 输出单个 JSON 对象，包含：

```text
sync_error_ms
loss_recovery_rate
p95_latency_ms
latency_samples
dropped_duplicates
gap_events
psi_version
output_sha256
rollback_count
exit_code
```

验收约束：

- 无损样例 `sync_error_ms <= 20`；
- `loss_recovery_rate = recovered / recoverable`，演示固定为 `1.0 >= 0.95`；
- P95 来自 21 次本机单次处理耗时样本，同时打印 `latency_samples`；
- 损坏样例报告 CC/缺口和截断，`exit_code=2`，且没有输出文件和 `.commit.json`；
- 恢复前后及重复运行的 `output_sha256` 相同；
- 成功为 `0`，输入损坏为 `2`，事务/内部错误为 `1`。

> 注意：直接运行编译后的 `medpipe` 时进程退出码遵循上述约定；`go run` 会把子进程的非零退出包装为 shell 退出码 `1` 并打印 `exit status 2`，但 JSON 中的 `exit_code` 仍是准确的 `2`。

## 支持的容器和编码

输入：

- MPEG-TS：188 字节固定包、PAT/PMT、PES/PTS、PCR adaptation field、连续性计数；
- MP4/ISOBMFF：使用 `github.com/abema/go-mp4` 解析 box 和样本表，支持读取本项目写出的 MP4 轨道。

输出：

- MP4（`ftyp/moov/mdat`、`stsd/stts/stsc/stsz/stco`）。

明确支持的编码：

| TS stream type | MP4 sample entry | 类型 |
| ---: | --- | --- |
| `0x1b` H.264/AVC | `avc1` | 视频 |
| `0x24` H.265/HEVC | `hvc1` | 视频 |
| `0x0f` AAC ADTS | `mp4a` | 音频 |
| `0x03` MPEG-1 Audio | `mp4a` | 音频 |

未知 stream type 或 MP4 sample entry 会返回 `unknown_codec` / `unmappable_track`，不会猜测封装。转封装只复制压缩样本字节，不做解码、重采样或重编码。

## 显式映射

```go
rules := []demux.MapRule{
    {FromPID: 0x0100, ToIndex: 1, Enabled: true}, // video
    {FromPID: 0x0101, ToIndex: 2, Enabled: true}, // audio
}
mappings, err := demux.SelectTracks(media, rules)
```

省略规则时按源顺序映射全部受支持轨道。

## 事务与一致性

每个输出目录包含 `medpipe.wal.jsonl`。阶段状态、输入摘要、映射摘要、临时文件、输出摘要和提交标记均先写 WAL 并 fsync。输出先写入目标目录中的 `*.tmp`，校验 go-mp4 box 可读性和摘要后通过同目录 `rename(2)` 原子替换，再写 `*.commit.json`。

恢复时：

- WAL 中 `prepared` 且摘要/可读性校验通过：补做原子提交；
- 摘要不匹配或输出不可读：删除临时文件并追加 rollback；
- 只有 `started` 记录：回滚未完成临时文件。

## 上限

- 单输入/TS 缓冲：64 MiB；
- MP4 输入：128 MiB；
- WAL：4 MiB；
- RTP 去重状态按窗口倍数有界淘汰。

## 包结构

```text
internal/clock      33 位 PCR/PTS 展开、统一毫秒时间、同步误差和 P95
internal/fault      统一错误码、事件和退出码
internal/rtp        pion/rtp 解析、序号窗口重排、去重、迟到/缺口事件
internal/ts         TS 包、CC、PCR、PAT/PMT 版本提交、PES 重组
internal/demux      TS/MP4 路由、媒体模型、显式映射
internal/mux        go-mp4 box 写入、无损样本复制、可读性校验
internal/txn        WAL、原子提交、提交标记、恢复和回滚
internal/samplegen  确定性正常/故障夹具
internal/demo       验收场景和 JSON 报告
cmd/medpipe         终端入口
```

## 开发检查

```bash
gofmt -w cmd internal
go vet ./...
go test ./...
```
