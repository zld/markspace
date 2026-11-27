# continuity — 直播主备源切换后的本地连续性分析内核

纯本地、纯终端的 Go 1.23 工具：统一分析音频码流结构（RTP/Opus、MP4/AAC
样本表）、视频关键帧间隔与损坏区段，支持中断续跑与结果溯源。样例数据全部
由代码按种子生成，不引用外部素材。

## 构建与验收

```sh
go mod tidy && go build ./... && go test ./...
go run ./cmd/continuity demo --seed 17 --out ./tmp/demo
# 中断续跑：
go run ./cmd/continuity demo --seed 17 --out ./tmp/resume --interrupt-after scan
go run ./cmd/continuity demo --seed 17 --out ./tmp/resume   # 从检查点续做，清单字节一致
# 一键验收：
sh scripts/acceptance.sh
```

注意：`go run` 会把子进程退出码 2 显示为 `exit status 2` 并以 1 退出；
需要精确退出码时请使用 `go build -o` 生成的二进制（验收脚本即如此）。

## 退出码

- `0` 完成，输入无损坏
- `2` 输入损坏但报告已生成（演示夹具自带丢包/重复/回绕/截断，故为 2）
- `1` 配置、依赖或恢复状态错误（含检查点输入/配置/版本不匹配、步骤失败、模拟中断）

## 模块

| 路径 | 职责 |
| --- | --- |
| `internal/timeline` | 整数采样时钟（禁止浮点累计）、轨道图与结构化错误模型 |
| `internal/ingest` | RTP 序号回绕展开/去重/重排窗口/缺口；Opus TOC 与 AAC 包时长 |
| `internal/mp4` | go-mp4 轨道/样本/时间基扫描，异常时间戳与损坏样本定位 |
| `internal/mp4/keyframe` | 关键帧间隔计算与过长间隔定位 |
| `internal/damage` | 损坏区段归并（保留包序号/样本偏移与起止时间） |
| `internal/pipeline` | 显式依赖图拓扑执行；检查点续跑与幂等重试 |
| `internal/manifest` | 时间对齐清单（确定性字节）；`provenance` 输入与步骤 SHA-256 |
| `internal/fixgen` | 确定性 RTP/MP4 夹具生成器 |
| `cmd/continuity` | 验收入口 `demo` |

## 行为要点

- Opus 按包内采样数（RFC 6716 TOC）、AAC 按 MP4 样本时基换算时长；未知或
  截断包报带轨道/阶段/偏移的结构化错误，不伪造音频。
- RTP 序号回绕后展开排序，重复包只计一次；重排窗口后仍缺失的序号记为缺口。
- 检查点绑定输入哈希 + 配置哈希 + 步骤版本；任一变化拒绝复用（退出码 1）。
- 清单时间统一为 48 kHz 整数刻度，对齐误差不超过 1 个音频采样刻度。
- 质量指标口径：结构性指标（RTP 序号/Opus TOC 时长与 MP4 样本表），非心理
  声学解码；无可解码包时明确报告"不可计算"。

## 实施边界

仅处理本地生成的 RTP/MP4 样例；不承诺实时转码或任意编解码支持。
