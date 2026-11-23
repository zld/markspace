# medfix — 直播切片时间戳修复与可随机访问索引库

区域直播分发平台的媒体处理库：在时钟精度、网络状态和封装各异的边缘节点间，
修复直播切片与跨轨定时元数据（字幕、章节、ID3、emsg、SEI）的时间戳，重建可
随机访问的索引，并留下可逐段复核的处理来源。纯本地、无网络依赖、无外部文件。

## 模块结构

| 包 | 职责 |
|---|---|
| `internal/clock` | 整数有理数时基、精确换算（无浮点累计）、RTP 32 位时间戳回绕扩展与歧义判定 |
| `internal/decode` | MP4 样本表/关键帧/emsg 解析（go-mp4）、RTP/ID3 解析（pion/rtp），流式回调 |
| `internal/fixture` | 自包含、确定性的 MP4 与 RTP/ID3 样例生成及故障注入 |
| `internal/timeline` | 轨道/事件/来源偏移/结构化错误模型，锚点与漂移修复、稳定去重、跨轨冲突 |
| `internal/index` | 版本化随机访问索引（关键帧/元数据/时间范围查询，溯源反查，原子落盘） |
| `internal/journal` | 哈希链阶段检查点、输入指纹、配置失效、幂等重试、偏移→片段溯源 |
| `internal/pipeline` | 解析→变换→验证→提交四阶段编排，原子检查点与中断恢复 |
| `cmd/medfix` | 终端演示与本地核验入口（`demo` / `verify`） |

## 构建与测试

```sh
go mod tidy
go build ./...
go test ./...
```

## 演示与验收命令

```sh
# 正常 + 截断 + 回绕歧义 + 变换后中断恢复样例
go run ./cmd/medfix demo --seed 42 --interrupt-after transform

# 同输入重复执行，验证逐片段 SHA-256 稳定
go run ./cmd/medfix verify --seed 42 --repeat 2
```

终端输出字段：`roundtrip_match_rate`、`keyframe_hit_rate`、`segments`、
`segment_sha256`、`resumed_stage`、`source_offset`、`invariant_failures`。

退出码：`0` 全部不变量与阈值通过；`1` 验收指标失败；`2` 参数或输入格式错误
（注：`go run` 会把非零退出码归一为 1，直接运行编译后的二进制可观察到 2）。

## 关键设计约束

- 每条轨道保留原始时基，经 `big.Int`/`big.Rat` 换算到统一纳秒刻度，禁止浮点累计。
- 回绕仅在序列证据可判定时扩展；无法判定的倒退返回带原始偏移的结构化错误。
- 截断字段（样本表、mdat、ID3、索引）绝不静默跳过，错误始终携带原始字节偏移。
- 重复事件按「来源偏移 + 内容哈希」稳定去重；跨轨事件超容差即标记冲突并阻止索引提交。
- 索引仅在提交阶段原子改名发布，任何阶段失败都不会留下半成品索引。
- 检查点损坏时回退到最近有效阶段；输入指纹或配置变化使旧检查点全部失效。
- 样本与包逐个流式处理，不将完整媒体载荷载入内存。
