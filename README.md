# jitterlab

离线复现不稳定网络下的实时音频接收行为，比较两条接收路径（本地 UDP、
确定性事件回放）经过**同一抖动缓冲控制器**的播放时序，并量化丢包隐藏
能否维持连续输出。纯标准库，Go 1.22+，无外部服务依赖。

## 构建与测试

```sh
go test ./...
go build ./cmd/jitterlab
```

## 使用

确定性回放（固定种子可复现）：

```sh
go run ./cmd/jitterlab replay --frames 1200 --seed 41 --loss-burst 7 \
  --reorder-depth 9 --duplicate-rate 0.03 --jitter-ms 18 --drift-ppm 120 --json
```

UDP 接收（仅绑定本机地址，超时后正常收尾并打印同样字段）：

```sh
go run ./cmd/jitterlab listen --addr 127.0.0.1:39090 --duration 5s --json
```

不加 `--json` 时输出终端文本摘要。退出码：0 正常结束，2 参数无效，
3 运行期不可恢复错误；单个坏包只计入 `malformed`，不影响退出码。

## 输出字段

- `watermarks`：按固定采样间隔记录的缓冲水位（时间、缓冲帧数、当时目标延迟）
- `received` / `duplicate_dropped` / `late_dropped` / `malformed`
- `played_frames` / `concealed_frames` / `max_conceal_run` / `unrecoverable_events`
- `continuity_ratio`：真实帧占全部输出槽位比例；无有效帧时为 `null`
- `target_delay_p50_ms` / `target_delay_p95_ms`：自适应目标延迟分位数

## 架构

```
cmd/jitterlab        参数解析、子命令组装、退出码
internal/protocol    自定义二进制包编解码与不可信输入校验
internal/receiver    UDP 收包(listen) 与离线事件回放(replay)，共用控制器
internal/jitter      重排窗口（去重/迟到/序号回绕）+ 播放时刻与水位控制
internal/conceal     由最近有效帧生成隐藏帧，标注连续性与不可恢复点
internal/metrics     水位曲线、计数器、目标延迟分位数
internal/scenario    带种子的事件生成：突发丢包/乱序/重复/抖动/时钟漂移
internal/report      JSON 与终端摘要
```

数据流：包解码 → 重排窗口 → 播放调度（目标延迟随 RFC 3550 抖动估计在
上下限内自适应）→ 缺失帧隐藏 → 指标汇总。所有定时逻辑基于事件时间或
注入时钟，测试不依赖真实网络时序；缓冲内存由窗口与载荷上限约束。

## 包格式（大端，21 字节头）

| 偏移 | 字段 |
|---|---|
| 0 | magic `0x4A4C` |
| 2 | stream id uint32 |
| 6 | 序号 uint32（模 2^32 比较） |
| 10 | 发送时间戳 int64（毫秒） |
| 18 | 帧类型 uint8（0 音频 / 1 舒适噪声 / 2 流标记） |
| 19 | 载荷长度 uint16 |
