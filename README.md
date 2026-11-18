# jitterlab

终端可运行的本地语音接收链路模拟器。它在回环 UDP 上发送自定义定长语音帧，
经过与线上一致的「解码 → 乱序窗口 → 自适应缓冲 → 丢包隐藏 → 指标」链路，
输出逐帧水位与汇总指标，用于复现弱网下的断续、延迟抬升与隐藏帧连续性。

不采集真实网络流量，不访问音频设备或任意用户文件；仅绑定 `127.0.0.1`。

## 快速开始

```sh
go test ./...

go run ./cmd/jitterlab replay \
  --frames 1200 --frame-ms 20 --target-ms 80 \
  --loss 0.04 --reorder 0.08 --duplicate 0.02 \
  --seed 41 --out /tmp/jitter.jsonl
```

输出约 1200 条 `frame` 行加 1 条 `summary` 行，退出码 0。

实时收发（本机回环，按真实时钟发送）：

```sh
go run ./cmd/jitterlab live --frames 600 --frame-ms 20 \
  --target-ms 80 --loss 0.04 --reorder 0.08 --duplicate 0.02 \
  --jitter-ms 20 --seed 41 --out /tmp/live.jsonl
```

## 架构

```
testkit (确定性包序列)
   ├── netem 回环 UDP ──► receiver/live   ─┐
   └── receiver/replay ──────────────────► ├─► packet 解码
                                           ├─► buffer.Window 乱序/去重/回绕
                                           ├─► buffer.Controller 自适应水位 + 播放时钟
                                           ├─► conceal 丢包隐藏
                                           ├─► metrics 汇总
                                           └─► report JSON Lines
```

- `internal/packet/codec.go`：32 字节定长帧（40 位扩展序号、采样/发送时间、CRC-32）。
- `internal/buffer/window.go`：乱序窗口、去重、40 位序号回绕、迟到包不覆盖已输出帧。
- `internal/buffer/controller.go`：实时与回放**共用**的播放控制器；按到达抖动平滑目标水位，
  有上下限与「每次更新最多调整一帧」的速率限制；超出上限时丢弃最旧帧以压回延迟。
- `internal/conceal/conceal.go`：先延展上一帧，再按固定衰减逐步减弱，超过上限输出静音标记
  并继续累计连续隐藏长度。
- `internal/receiver/guard.go`：连续损坏达到阈值时终止流（退出码 3）。
- `internal/netem/udp.go`：仅回环的 UDP 收发，按计划时间发送，可被 stop 信号中断。
- `internal/report/jsonl.go`：逐帧流式输出，`--out` 为空或 `-` 时写标准输出。

## 参数

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--frames` | 1200 | 帧数（1..200000） |
| `--frame-ms` | 20 | 帧时长（1..500） |
| `--target-ms` | 80 | 目标缓冲深度 |
| `--max-wait-ms` | 240 | 最大等待；不得小于 target 或小于一帧 |
| `--window` | 64 | 乱序窗口帧数（1..4096） |
| `--seed` | 0 | 扰动种子；相同参数与种子结果完全一致 |
| `--loss` / `--reorder` / `--duplicate` | 0 | 丢包、乱序、重复概率 |
| `--burst-loss` / `--burst-len` | 0 | 突发丢包起始概率与每次突发长度 |
| `--jitter-ms` / `--delay-ms` | 0 | 附加随机抖动 / 固定路径延迟 |
| `--drift-ppm` | 0 | 接收时钟漂移（正数表示接收侧变快） |
| `--corrupt` / `--truncate` / `--bad-version` | 0 | 校验失败、截断、未知版本注入概率 |
| `--max-invalid-run` | 16 | 连续损坏终止阈值 |
| `--out` | stdout | JSON Lines 路径 |

仅 `replay`：`--reorder-delay`、`--duplicate-delay`。
仅 `live`：`--duration`、`--read-timeout`。

## 输出

帧行：

```json
{"type":"frame","frame":{"frame":1,"t_ms":120,"seq":0,"kind":"real","buffer_ms":80,"target_ms":80}}
```

`kind` 为 `real` / `extend` / `decay` / `silence`；隐藏帧带 `conceal_run`。

汇总行包含：

- `input_packets`、`valid_packets`、`invalid_packets`，以及按原因拆分的
  `truncated_packets`、`version_errors`、`length_errors`、`checksum_errors`；
- `duplicate_packets`、`reordered_packets`、`dropped_packets`（= 传输丢失 + 窗口放弃 + 为压延迟主动丢弃）；
- `concealed_frames`、`silent_frames`、`max_conceal_run`、`recoveries`、
  `continuity_delta`（隐藏结束回到真实音频时的平均样本跳变）；
- `buffer_ms`、`buffer_ms_min`、`buffer_ms_max`、`buffer_ms_avg`、`target_ms`；
- 发送侧账目 `lost_packets`、`injected_duplicates`、`injected_reordered`、`injected_faults`，
  可核对 `input_packets = frames - lost_packets + injected_duplicates`。

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 完成 |
| 2 | 参数错误（含互相矛盾的时延、越界概率、未知参数） |
| 3 | 输入或协议损坏（连续损坏达到阈值、读错误） |
| 4 | 输出失败（路径不可写、写入中途失败） |

## 设计取舍

- **同一控制器**：实时与回放都调用 `buffer.Controller`，因此两者的水位、
  丢弃与隐藏行为可直接对比；回放用固定步长时钟，保证同种子同统计。
- **迟到包不覆盖**：播放点只前进，已被隐藏或被播放的序号再次到达时计入重复或过期丢弃。
- **隐藏连续性**：`max_conceal_run` 统计完整连续隐藏（含超过上限的静音段），
  `continuity_delta` 只在真实音频恢复时采样，避免把静音计成「平滑」。
- **资源边界**：帧数、窗口、包长、缓冲内存均有上限；所有随机性来自种子化
  SplitMix64，可在任意 Go 版本复算。
