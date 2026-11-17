# jbcheck

本地终端工具：用可复现的网络扰动检查 48 kHz 单声道 PCM16（10 ms 帧）接收连续性。
纯标准库实现，无外部服务依赖，UDP 仅监听 127.0.0.1。

## 构建与测试

```sh
go test ./... && go build -o /tmp/jbcheck ./cmd/jbcheck
```

## 演示

```sh
# 轨迹回放（虚拟时钟，瞬时完成）
/tmp/jbcheck demo --path trace --seed 73 --frames 1000 \
  --loss 0.06 --burst 0.02 --reorder 0.10 --duplicate 0.03 --curve 100ms

# 环回 UDP（实时，约 10 秒）
/tmp/jbcheck demo --path udp --seed 73 --frames 1000 \
  --loss 0.06 --burst 0.02 --reorder 0.10 --duplicate 0.03 --curve 100ms \
  --listen 127.0.0.1:0
```

## 工作原理

- `internal/sim` 按固定种子生成正弦 PCM 帧，并施加时延抖动、独立丢包、
  突发丢包、乱序（额外 5–20 ms 时延）与重复包；同一参数组合产生完全相同的轨迹。
- `internal/packet` 定义包头（magic、16 位序号、32 位采样时间戳、长度、CRC32），
  拒绝截断、超长、校验失败的数据包。
- `internal/jitter` 是 UDP 与轨迹共用的控制器：RFC 3550 抖动估计，
  缓冲目标在 2–12 帧间以迟滞 + 渐进方式调整；队列有界，满时先按播放时限
  淘汰过期帧；支持启动预填充、长静默重缓冲与时钟跳变重锚定。
- `internal/plc` 在缺帧时以前一帧衰减生成隐藏帧，连续缺帧 5 帧后切换为
  确定性低幅噪声；恢复时记录边界跳变。
- `internal/metrics` 输出丢包、重复、迟到、溢出、隐藏帧比例、最大连续隐藏
  帧数、恢复跳变 P95，以及按 `--curve` 分桶的 `time_ms,queue_frames` 水位曲线
  （`--format text|csv`）。

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 完成 |
| 2  | 参数错误（含非环回 --listen） |
| 3  | 输入损坏（无任何可解码包） |
| 4  | I/O 失败（套接字、标准输出） |
