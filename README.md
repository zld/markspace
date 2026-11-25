# mediasync

离线多语言游戏对白/字幕/音效资产的可复用 Go 处理内核。它在纯本地、无外部
服务、无图形界面的前提下，完成：

- MXF / MP4 元数据读取与冲突裁决（旋转、裁剪、色彩、时间基）
- RTP 序号去重、16 位回绕扩展、乱序重排、等待窗口内缺包恢复
- 随带宽自适应的抖动缓冲（目标水位带上下限）
- 音频 / 视频 / 字幕到统一整数时间线的对齐与音画漂移校验
- 每阶段 WAL 先写、原子提交不可变产物清单、失败回滚与重启恢复
- 离屏产物的 SHA-256、像素统计、音频峰值响度、时间对齐误差
- 确定性量化摘要，重算可逐项核验

## 技术栈与依赖

- Go 1.23（本机工具链更高版本亦可，go.mod 固定 `go 1.23`）
- `github.com/abema/go-mp4 v1.2.0`：MP4 box 解析
- `github.com/pion/rtp v1.8.10`：RTP 序号与时间戳解析

依赖版本在 `go.mod` 固定，运行时只读写本地文件，不执行输入中的脚本或外部命令。

## 目录结构

```
cmd/mediasync/        验收入口（demo / verify / recover），核心逻辑均在 internal
internal/clock/       统一整数刻度主时间线（90 kHz ticks）
internal/model/       共用错误记录模型（偏移/轨道/时间区间/错误码）
internal/metadata/    MXF、MP4 解析与 MXF>MP4>缺省 优先级裁决
internal/rtp/         序号扩展、去重重排、抖动缓冲、恢复引擎
internal/sync/        音画字幕对齐与漂移阈值失败
internal/txn/         WAL、原子提交、清单哈希、回滚、重启恢复
internal/render/      确定性离屏帧/音频渲染与统计
internal/report/      确定性 JSON 摘要、哈希与逐项比较
internal/fixture/     随代码生成的样例素材与故障注入
internal/pipeline/    可复用编排内核（被 CLI 薄封装）
```

元数据、RTP 恢复、事务与产物核验为三个协作子系统；`clock` 与 `model` 被至少
两个子系统共用，且各自带独立测试。

## 构建与测试

```sh
go mod tidy
go build ./...
go test ./...
```

## 验收命令

成功素材（自含重复、乱序、突发丢包、变化带宽）：

```sh
go run ./cmd/mediasync demo --seed 7 --out /tmp/mediasync-a
go run ./cmd/mediasync verify --dir /tmp/mediasync-a
```

截断故障（报告截断偏移并以非零码退出），随后回滚续跑：

```sh
go run ./cmd/mediasync demo --case truncated --out /tmp/mediasync-b
go run ./cmd/mediasync recover --dir /tmp/mediasync-b
```

`verify` 重算并逐项比较：哈希相同、时间对齐误差不超过 1 ms。

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 成功 / 核验通过 |
| 1  | 输入或处理失败（含截断、损坏包、漂移超阈、清单不匹配） |
| 2  | 参数错误 |

## 可配置上限

内存（重排缓冲 `MaxBuffered`）、缺包等待窗口（`WaitWindow`）、输出规模
（渲染 `MaxFrames` / `MaxSamples`）、音画漂移阈值（`MaxDriftMillis`）与抖动
目标上下限（`MinTarget`/`MaxTarget`）均有显式配置与默认上限，缺包窗口耗尽只
记录缺口、绝不伪造媒体内容。
