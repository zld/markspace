# continuity

直播主备源切换后的交接媒体（MP4/MOV）时间对齐分片内核。读取带时间戳的
容器文件，把各轨道时间统一换算到纳秒时基，按共同关键帧时间点切片，生成
多码率清单，并由独立校验器核对交付约束。纯本地、纯终端，无外部服务依赖。

## 子系统

| 包 | 职责 |
|---|---|
| `media` | 统一纳秒时基（有理数换算，溢出即失败）、共享错误模型（轨道/偏移/可恢复性）、容器探测、样本与关键帧索引 |
| `fixture` | 自生成正常/截断/时间倒退/关键帧缺失/声道·采样率·色彩冲突样例 |
| `planner` | 处理前评估候选分片时长、缓冲上限与并发数；声道/采样率/色彩冲突的显式转换计划 |
| `worker` | 有界并发工作池 |
| `segment` | 按共同关键帧时间点切片；按输入哈希+规划参数+片段哈希记录状态，中断后仅复用哈希匹配的分片 |
| `manifest` | 多码率清单（表示层参数、逐片 SHA-256） |
| `verify` | 独立校验：时间单调、表示层边界差 ≤ 一个统一时钟刻度、轨道参数、清单引用 |
| `cmd/continuity` | 命令入口，驱动上述库并打印验收指标 JSON |

## 构建与测试

```sh
brew install go@1.23 && export PATH="$(brew --prefix go@1.23)/bin:$PATH"
go mod download && go build ./... && go test ./...
```

## 演示

```sh
go run ./cmd/continuity demo --case valid --representations 2 --segment 2s
```

输出 JSON：`roundtrip_rate`（1.000）、`keyframe_hit_rate`（1.000）、
`segment_hashes`（每片 64 位十六进制 SHA-256）、`aligned_boundary_delta_ns`
（不大于一个统一时钟刻度）、`verified`（true）。

```sh
go run ./cmd/continuity demo --case corrupt-box      # 报告截断偏移，退出码 2
go run ./cmd/continuity demo --case missing-keyframe # 报告表示层未命中，退出码 3
go run ./cmd/continuity demo --case resume           # 复用/重算计数，清单哈希不变，退出码 0
```

注意：`go run` 会把子进程退出码映射为 1（stderr 打印 `exit status N`）。
需要直接观察退出码时请使用构建产物：

```sh
go build -o bin/continuity ./cmd/continuity
./bin/continuity demo --case corrupt-box; echo $?   # 2
```

## 退出码

| 码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 参数或预算错误（预算不足时列出候选及拒绝原因，不创建部分清单） |
| 2 | 容器解析失败（截断 box / 非法时间基 / 时间倒退 / 溢出），报告偏移与轨道 |
| 3 | 关键帧未命中（某表示层缺少共同切点关键帧，整体失败，不发布不齐清单） |
| 4 | 独立校验失败（不标记完成） |

## 运行参数

`--representations`（1..8）、`--segment`（如 2s）、`--workdir`、
`--max-buffer`、`--max-concurrency`（1..64）、`--target-channels`、
`--target-sample-rate`。并发、缓存与样例大小均受参数上限约束；输入输出
限于命令指定的本地路径。

兼容范围为 go-mp4 可解析的 MP4/MOV 结构，不承诺修复任意损坏媒体。
