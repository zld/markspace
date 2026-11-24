# mediarun — 媒资归档恢复处理内核

面向长期归档的 Matroska/WebM 历史素材与本地 RTP 样例：流式解析、统一时基、
音频归一（采样率/声道/响度）、RTP 丢包恢复、背压调度、断点续跑与来源核验。
纯终端形态，无外部服务依赖。

## 构建与测试

```sh
brew install go@1.23   # 或任意 Go >= 1.23
go mod tidy && go build ./... && go test ./...
```

## 验收流程

```sh
# 生成样例（正常/截断簇/超窗丢包/背压）并执行，在 audio 阶段后中断 → 退出码 10
go run ./cmd/mediarun demo --seed 7 --rate 48000 --layout stereo --lufs -16 --loss 0.02 --interrupt-after audio
# 断点续跑，可重复执行，输出 JSON 指标且 result_sha256 相同
go run ./cmd/mediarun resume --seed 7
```

注：`go run` 会把子进程退出码打印为 `exit status 10` 并以 1 退出；
直接执行 `go build` 产出的二进制可获得真实退出码。

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 成功，指标达标 |
| 1  | 测试/指标不达标或运行错误 |
| 2  | 输入或参数摘要与检查点不匹配，拒绝续跑 |
| 3  | 致命解析失败（如 EBML 头损坏） |
| 10 | 按 `--interrupt-after` 在阶段边界中断，检查点已落盘 |

## 模块

- `internal/ebml`：流式 EBML 解析（未知长度 Segment/Cluster、损坏块隔离）、
  轨道映射、统一媒体时基（缺失时间戳/回绕的确定性修正，共享错误模型）
- `internal/audio`：线性插值重采样、声道布局归一、RMS 近似响度控制
- `internal/rtp`：序号缺口窗口恢复；超窗缺包计入 unrecovered，不伪造成功
- `internal/pipeline`：有界队列串联各阶段，下游变慢即向上游施加背压
- `internal/checkpoint`：绑定输入/参数摘要与阶段偏移的原子检查点
- `internal/provenance`：来源摘要 + 参数 + 错误位置清单；go-mp4 结果结构核验
- `internal/testkit`：确定性样例生成（种子驱动，不引用外部媒体）

## 依赖

- `github.com/abema/go-mp4` v1.7.4（任务书写作 go-mp4 v1.31.0；该版本号在
  上游不存在，v1.7.4 为当前最新发布，API 用途一致：结果结构核验）
- `github.com/at-wat/ebml-go` v0.17.1（EBML 头校验）
- `github.com/pion/rtp` v1.8.9（RTP 包编解码）
