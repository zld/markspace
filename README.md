# Replay Kernel

赛事回放关键事件切分内核。所有夹具均由 Go 代码确定性生成，不引用外部音视频素材或网络服务。

## 模块

- `internal/clock.go`：统一 90 kHz 媒体时钟、33-bit PCR/PTS 回绕展开、时间戳倒退错误。
- `internal/errors.go`：带 PID、半开时间范围、包序号和是否可恢复的统一错误/缺口模型。
- `internal/rtp_reorder.go`：基于 `github.com/pion/rtp` 的序号窗口解析、去重、重排、丢失/迟到记录和自适应等待。
- `internal/ts_parse.go` / `ts_encode.go`：0x47 同步字节、188 字节包长、PCR、连续计数、截断与同步丢失校验。
- `internal/psi.go` / `psi_encode.go`：PAT/PMT 版本、CRC、旧映射保留和不完整版本期间的切分暂停。
- `internal/tracks.go` / `media_wire.go`：共享时钟下的规范化音视频/事件轨道。
- `internal/event_cut.go`：按关键事件切半开区间 `[start,end)`，关联缺口与静音。
- `internal/silence.go`：可配置 PCM 静音阈值/最短时长，并从静音区间扣除缺口覆盖范围。
- `internal/fingerprint.go`：稳定片段指纹、重复内容分组。
- `internal/mp4_segment.go` / `delivery.go`：用 `github.com/abema/go-mp4` 派生确定性 MP4/ISOBMFF 片段，并按源摘要缓存。
- `internal/resume.go`：有大小上限的原子检查点，记录阶段、输入包序号、时钟状态和轨道时间范围。
- `internal/manifest.go`：清单写出及哈希、文件大小、时间范围、顺序和缺口引用逐项校验。
- `internal/fixture.go`：clean / damaged / malformed 夹具生成和网络损伤。
- `internal/pipeline.go`：传输、解析、切分、检查点、恢复、交付和清单的统一编排。
- `cmd/demo/main.go`：纯终端验收入口。

## 构建与测试

```bash
go mod tidy
go build ./...
go test ./...
```

也可运行完整验收脚本：

```bash
./scripts/acceptance.sh
```

## 验收命令

干净夹具：

```bash
go run ./cmd/demo --fixture clean --out /tmp/replay-a
```

关键输出：

```text
stages=ingest,parse,cut,manifest
wraps=1
cuts=2
silence_ms=1000
created=2 reused=0 invalidated=0
manifest_valid=true
```

重复执行同一命令会得到 `created=0 reused=2`。改变源摘要：

```bash
go run ./cmd/demo --fixture clean --out /tmp/replay-a --mutate-source
# invalidated=2
```

可恢复中断与续跑：

```bash
go run ./cmd/demo --fixture damaged --out /tmp/replay-b --stop-after cut
go run ./cmd/demo --fixture damaged --out /tmp/replay-b --resume
```

第一条输出 `checkpoint=cut`、`gap_events=2` 和内部 `exit_code=2`；第二条输出：

```text
resume_stage=manifest
resume_packet=<检查点输入包序号>
manifest_valid=true
```

完整运行与“cut 后检查点 + resume”生成相同的片段内容哈希、文件哈希和清单。

畸形包：

```bash
go run ./cmd/demo --fixture malformed --out /tmp/replay-mal
```

输出 `packet_too_large`，进程状态码为 1。

写出后篡改并执行最终校验：

```bash
go run ./cmd/demo --fixture clean --out /tmp/replay-tamper --tamper-output
go run ./cmd/demo --out /tmp/replay-tamper --verify-manifest
```

两者均因哈希校验失败返回状态码 1。

## 退出码约定

- `0`：完成且清单校验通过。
- `2`：到达 `--stop-after cut` 等已成功落盘的可恢复检查点。
- `1`：不可恢复的畸形输入、路径越界、检查点损坏或最终清单校验失败。

注意：`go run` 工具本身会把编译产物的任意非零退出状态统一包装成 `1`，并额外打印 `exit status 2`；程序输出中的 `exit_code=2` 是内核契约。直接运行 `go build -o /tmp/replay-demo ./cmd/demo && /tmp/replay-demo ...` 时，可恢复中断严格返回 2。`scripts/acceptance.sh` 同时验证这一点。

## 安全与资源边界

- 网络包和检查点/清单均有大小上限。
- 所有交付文件路径经 `SafeJoin` 解析，拒绝绝对路径和 `..` 穿越。
- JSON/检查点和片段通过同目录临时文件加原子重命名写出。
- RTP 重排窗口和缓存映射均为有限结构。
- 丢失数据只记录为 `GapEvent`，不会补造媒体样本。
- 输出目录中的派生物可删除重建；规范化过程复制输入字节，不修改源资产。

## Go 版本说明

`github.com/pion/rtp v1.10.6` 的 `go.mod` 声明 `go 1.24.0`，因此本模块的 `go` 指令由 `go mod tidy` 固定为 `1.24.0`。在安装 Go 1.23 且 `GOTOOLCHAIN=auto`（默认）时，Go 命令会按工具链机制获取兼容工具链；依赖版本仍由 `go.mod` / `go.sum` 固定。
