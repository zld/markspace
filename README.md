# jitterlab

本机可复现的语音链路抖动实验室：按种子生成带损伤（延迟、乱序、重复、
随机/突发丢包、序号回绕）的语音包流，经 127.0.0.1 的 UDP 或 TCP 接收，
进入两条路径共用的自适应缓冲控制器，按 20 ms 播放时钟取帧，缺帧走 PLC
隐藏，最终以 JSON Lines 输出水位曲线与汇总。

## 环境

```sh
brew install go
export GOTOOLCHAIN=go1.22.12   # 固定工具链
go version                     # go1.22.12 darwin/arm64 或 amd64
```

注意：在新版 macOS（Darwin 27+）上，go1.22.12 的 cgo 链接产物缺少
`LC_UUID`，无法被 dyld 加载。本仓库只用到纯 Go 标准库路径，执行一次
`go env -w CGO_ENABLED=0` 即可让 `go test` / `go build` 正常运行。

## 运行

```sh
go test ./...
go build -o /tmp/jitterlab ./cmd/jitterlab
/tmp/jitterlab demo --transport both --seed 424242 --duration 8s \
    --frame 20ms --curve 100ms --format jsonl
```

- `--transport`：`udp` / `tcp` / `both`（两条路径独立状态、共用控制器实现）
- `--seed`：场景随机种子，同一种子完全可复现
- `--curve`：水位采样间隔（每 100 ms 一条 `waterline` 记录）
- 退出码：0 完成；2 参数错误；3 回环收发失败；4 自检不变量失败

## 输出

`waterline` 记录：`transport`、`t_ms`、`queued_frames`、`target_frames`。
`summary` 记录：`frames_processed`、`concealed_frames`、
`discarded_packets`（duplicate/late/overflow/malformed/evicted）、
`plc_continuity_pct`、`plc_boundary_jump_p95`。

## 布局

- `internal/wire`：带长度上限与 CRC32 校验的包头编解码
- `internal/ingest`：UDP 数据报校验；TCP 长度前缀帧（拆包/截断/非法长度）
- `internal/jitter`：共享缓冲控制器（自适应水位）+ 序号窗口（回绕/去重/迟到）
- `internal/plc`：隐藏帧生成（超限渐隐至静音）与边界连续性统计
- `internal/sim`：种子化损伤场景生成（保证序号回绕至少一次）
- `internal/report`：JSONL 水位与汇总输出
