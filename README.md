# jitter — 弱网语音帧抖动缓冲本地验证工具

纯终端、仅 Go 标准库。用可复现的模拟数据比较 UDP 与 trace 两条接收路径经过
同一抖动缓冲控制器后的播放结果，定位声音断续来自缓冲策略还是丢包隐藏。

## 构建与测试

```sh
go mod download
go test -race ./...
go build -o /tmp/jitter ./cmd/jitter
```

## 演示（验收命令）

```sh
/tmp/jitter demo --path both --seed 41 --frames 600 \
  --loss 0.04 --reorder 0.08 --duplicate 0.02 --waterline-every-ms 200
```

输出水位 CSV（`path,elapsed_ms,queued_frames,target_frames`）与每路径一行 JSON
摘要（`frames_out`、`drops_duplicate`、`drops_late`、`concealed_frames`、
`continuity_ok_ratio`、`max_boundary_jump` 等）。固定种子输出完全一致。
`--csv-out FILE` 可把 CSV 写入显式指定的文件。

## 实时接收

```sh
/tmp/jitter listen --port 5005   # 仅绑定 127.0.0.1，Ctrl-C 输出最终汇总
```

## 退出码

0 完成；1 运行/网络错误；2 参数错误。

## 结构

- `internal/wire`：48 kHz / 20 ms / 单声道 PCM16 包格式，长度+校验和+采样数校验
- `internal/ingress`：loopback UDP 接收与可复现 trace 事件生成（丢包/乱序/重复）
- `internal/buffer`：共享控制器——排序、去重、16 位序号/32 位时间戳回绕、
  64 帧窗口、2–12 帧自适应目标水位（迟滞+渐进扩缩）、300 ms 停顿重对齐
- `internal/plc`：波形衰减隐藏（最多连续 3 帧后静音）与边界跳变统计
- `internal/clock`：单调真实时钟与可控虚拟时钟
- `internal/metrics`：水位曲线记录与连续性汇总
- `internal/demo`：双路径种子场景编排
