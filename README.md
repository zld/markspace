# mediad — 多机位现场音频后期处理内核

纯本地、无外部服务依赖的 Go 音频处理内核。统一 48 kHz 时间基准，处理 RTP 包
重复/乱序/突发丢失，完成采样率转换、声道归一与响度控制，并提供可复核的中断
恢复证据与交付校验和。

## 模块结构

| 包 | 职责 |
|---|---|
| `internal/clock` | RTP 16/32 位回绕展开、迟到 RTCP SR 时钟映射、提交后时间线不回退 |
| `internal/ingest` | 长度分帧读取、RTP（pion）/RTCP（RFC 手写）解析，错误携带 `code/offset/stage` |
| `internal/reorder` | 可配置乱序窗口、去重、超窗迟到丢弃、缺包与突发丢失追踪（不伪造样本） |
| `internal/audio` | 分块线性重采样（短尾边界）、单/立体声归一、目标 LUFS 增益与 0 dBFS 限幅 |
| `internal/checkpoint` | 原子检查点（temp+fsync+rename）、配置摘要与输入前缀逐字节校验 |
| `internal/pipeline` | 串接各子系统、按已验证音频块提交、中断恢复不变量 |
| `internal/fixture` | 自生成正常/重复乱序/突发丢失/回绕/截断/畸形长度样例 |
| `internal/manifest` | 用 go-mp4 写交付元数据 MP4 容器（ftyp/moov/mdat） |
| `cmd/mediad` | 终端入口：`--generate` / `inspect` / `process` |

## 构建与测试

```sh
go mod tidy && go build ./... && go test ./...
```

## 验收命令

```sh
# 生成并处理正常流
go run ./cmd/mediad --generate demo --out /tmp/normal.pcap
go run ./cmd/mediad process --in /tmp/normal.pcap --out /tmp/normalized.pcm \
  --rate 48000 --layout stereo --target-lufs -16 --checkpoint /tmp/job.ckpt

# 生成损坏流并定位（误差 ≤ 1 字节）
go run ./cmd/mediad --generate corrupt --out /tmp/corrupt.pcap
go run ./cmd/mediad inspect --in /tmp/corrupt.pcap
# -> error_code=TRUNCATED offset=... stage=parse

# 中断后续跑：resume_ms < 1000，续跑 SHA-256 与不中断结果一致
go run ./cmd/mediad process --in /tmp/normal.pcap --out /tmp/resumed.pcm \
  --checkpoint /tmp/job.ckpt --interrupt-after 5000
go run ./cmd/mediad process --in /tmp/normal.pcap --out /tmp/resumed.pcm \
  --checkpoint /tmp/job.ckpt
```

输出字段形态：

```
packets=12029 duplicates=32 reordered=231 gaps=3 samples_in=1151712 \
samples_out=1151712 peak_dbfs=-12.1 clipped=0 sha256=<64 位十六进制>
```

## 退出码

| 码 | 含义 |
|---|---|
| 0 | 成功（含按边界的有意中断） |
| 2 | 输入损坏或参数错误 |
| 3 | 检查点配置/输入前缀不匹配（附带偏移） |
| 4 | 内部验证失败 |

## 关键不变量

- **不静默伪造样本**：缺包仅产生 `KindGap` 事件与间隙统计，不写入假音频。
- **时间线不倒退**：已 `Commit` 的媒体位置被冻结，迟到/矛盾 SR 无法回退。
- **确定性**：相同输入与参数重复运行产出相同 PCM 与 SHA-256（有测试守护）。
- **恢复一致性**：检查点仅在完整音频块验证后提交；续跑重建输出哈希并追加
  后缀，最终校验和等于一次不中断运行。
- **资源约束**：单帧上限 64 KiB；检查点与内存占用远低于 256 MiB。

## 依赖说明

- `github.com/pion/rtp v1.8.11`：RTP 头部解析。
- `github.com/abema/go-mp4 v0.13.0`：MP4 清单容器。需求所列 `go-mp4 v1.13.0`
  在公开仓库不存在（abema/go-mp4 当前最高 v1.7.4，可用版本含 v0.13.0），
  故采用语义最接近的 v0.13.0 并在 `go.mod` 固定。
- RTCP 按 RFC 3550 手写最小 SR 解析，未额外引入 pion/rtcp。

支持 `darwin/arm64` 与 `darwin/amd64`，仅使用标准库 `testing`。
