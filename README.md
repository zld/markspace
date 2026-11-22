# mediapipe

可复用的 Go 媒体处理库：汇聚远程录音棚/剪辑站上传的 HLS/DASH 多轨素材，
在统一整数时间轴上完成分片校验、RTP 丢包恢复、重采样/声道映射/响度归一，
并确定性归并输出。损坏素材的恢复过程全程留痕、可复算。

## 子系统

| 包 | 职责 |
|---|---|
| `internal/clock` | 整数媒体刻度（48 kHz）、半开区间运算、RTP 16 位序号回绕、无漂移重采样计数器、共享结构化错误 |
| `internal/manifest` | HLS/DASH 清单解析；拒绝路径逃逸/重复序号/非正时长；缺口按显式策略标记为 Missing，不静默缩短时间线 |
| `internal/ingest` | 分片校验（go-mp4 盒结构、长度、校验和）、截断检测、最多两次重试、恢复记录 |
| `internal/rtp` | 有界窗口乱序/重复/丢包恢复，超窗丢包标记不可恢复 |
| `internal/audio` | 流式重采样（跨分片保状态，误差 <1 帧）、声道矩阵映射与静音补位、LUFS 响度与真峰值限制 |
| `internal/pipeline` | 有界并发工作池（背压 + 取消传播）、按清单序确定性归并 |
| `cmd/mediatool` | 演示与验收入口、五类故障样例生成器 |

## 验收

```sh
go mod tidy && go build ./... && go test ./...
go run ./cmd/mediatool demo --seed 23 --tracks 4 --workers 3 --faults all
go run ./cmd/mediatool verify --seed 23 --runs 2
```

退出码：0 通过；1 指标/不变量失败；2 参数或输入无效。

约束：单轨 30 分钟、64 轨、单分片 64 MiB、并发 1–8、内存队列有界；
仅处理本地清单与分片，不访问外部服务。
