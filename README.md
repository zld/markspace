# mediapack

可复用的 Go 媒体处理内核：按轨道依赖确定处理顺序，合成时间线并离屏导出可核验的帧与清单。纯终端形态，无外部服务依赖。

## 构建与验收

```sh
go mod tidy && go build ./... && go test ./...
go run ./cmd/mediapack demo --seed 17 --out /tmp/mediapack-demo --resume
go run ./cmd/mediapack demo --seed 17 --out /tmp/mediapack-demo --resume   # 帧哈希与清单摘要一致
go run ./cmd/mediapack verify-fixtures
# 或一键：scripts/accept.sh
```

退出码：0 通过；2 参数错误；3 素材或依赖图损坏；4 续跑校验失败。

## 架构

数据流：容器/RTP 解析 → 时间线与依赖图校验 → 按序合成 → 帧与质量统计 → 清单和溯源记录。各阶段共用整数纳秒时基与统一错误分类（`merr`）。

| 模块 | 职责 |
| --- | --- |
| `timeline/clock.go` | 统一纳秒时基、采样间隔换算、RTP 序号/时间戳回绕扩展、单调性检查 |
| `timeline/graph.go` | 有向无环轨道图：重复 ID、缺失依赖、循环依赖拒绝，确定性拓扑排序 |
| `timeline/clip.go` | 半开区间剪辑、转场校验与按时间线顺序的重叠混合权重 |
| `media/mp4.go` | go-mp4 解封装轨道与样本表；截断样本/无效索引记录损坏位置并停该轨 |
| `media/rtp.go` | pion/rtp 解析；乱序重排、去重、序号回绕、超阈值缺口形成显式空洞 |
| `render/compositor.go` | 按依赖顺序在输出网格上合成 720p 帧，空洞帧显式标记不伪装 |
| `render/hash.go` | 帧哈希（含网格位置）与质量统计聚合 |
| `manifest/manifest.go` | 带时间基准的导出清单、规范摘要、跨阶段一致性校验 |
| `pipeline/runner.go` | 阶段编排：有效检查点复用，损坏/过期检查点回退并丢弃下游产物 |
| `pipeline/checkpoint.go` | 检查点原子落盘（临时文件+rename），摘要链绑定输入与参数 |
| `pipeline/provenance.go` | 输入摘要、参数与产物关系链，可重算验证 |
| `demo/` | 演示输入生成（种子确定）、四阶段流水线、五类故障夹具 |
| `cmd/mediapack` | 终端入口：`demo` / `verify-fixtures` |

## 幂等与恢复

- 相同输入摘要+参数的重跑产生相同帧哈希、清单摘要与溯源链（内容全部从种子确定性派生，无墙钟/随机泄漏）。
- 检查点按阶段原子落盘并携带摘要链；进程中断后从最后一个校验通过的阶段续跑。
- 损坏检查点回退到前一阶段，其后的产物检查点被丢弃重建。
- 输入或参数改变时旧检查点全部失效，不得复用。

## 约束

- 文件写入限定输出目录（`safefs` 拒绝路径穿越），检查点与产物均带摘要。
- 输入仅支持本地生成的 MP4 样本与 RTP 包记录，不做任意编码格式解码。
- 依赖固定：go-mp4 v1.2.0、pion/rtp v1.8.9（见 `go.sum`）。
