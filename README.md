# mediaqc — 广播母版本地质检与修复库

纯本地、纯终端的 Matroska/WebM 后期处理工具：流式解析、时间戳修复、
可随机访问索引重建、背压执行、断点续跑与可重复核验的 JSON 报告。
不重新编码音视频，不读取外部样本，不访问在线服务。

## 构建与测试

```sh
brew install go
go mod tidy
go build ./...
go test ./...
```

依赖版本锁定在 `go.mod` / `go.sum`：

- `github.com/abema/go-mp4` **v1.7.4**（输出容器结构；上游最高版本即
  v1.7.4，需求中写的 v1.16.0 在上游不存在，已钉到可复现的最新版）
- `github.com/pion/rtp` **v1.8.9**（RTP 序号重排与丢包恢复）

## 使用

```sh
# 生成确定性样例（正常 + 五类故障），全部随代码生成
go run ./cmd/mediaqc generate --dir ./tmp/samples

# 处理正常母版：有界队列 + 慢消费者背压 + 断点续跑
go run ./cmd/mediaqc process --input ./tmp/samples/normal.webm \
  --output ./tmp/out.webm --queue 4 --consumer-delay 20ms --resume

# 修复时间戳回绕样例
go run ./cmd/mediaqc process --input ./tmp/samples/corrupt-wrap.webm \
  --output ./tmp/recovered.webm --resume

# 验收自检：正常/损坏/恢复三路径 + 五类故障 + 重复运行一致性
go run ./cmd/mediaqc selfcheck --dir ./tmp/samples
```

注意：`go run` 会把非零退出码吞成 1；需要核验退出码契约时请使用
`go build -o mediaqc ./cmd/mediaqc` 后的二进制。

## 退出码

| 码 | 含义 |
|----|------|
| 0 | 成功 |
| 2 | 输入损坏（截断/尺寸越界/同步超限）或轨道映射/编码错误 |
| 3 | 可恢复中断（取消/SIGINT），已留 checkpoint，可 `--resume` |
| 1 | 内部错误 |

## 架构（四个协作子系统）

```
输入字节流 → ebml 扫描器 → 摘要绑定的索引 → timeline 修复 →
stream 执行器（有界队列）→ remux 封装器 → 原子提交
                        ↘ 错误模型(Fault) → metrics JSON 摘要 + 退出码
```

- `internal/ebml`：`reader.go` 元素头解析（未知长度、越界、截断均
  带字节偏移与稳定错误类别）；`scan.go` 轨道与块边界扫描；
  `index.go` 以输入 SHA-256 绑定的偏移索引（版本+校验和，失配即弃）。
- `internal/timeline`：`clock.go` 统一纳秒时钟；`repair.go` 非零起点
  重定基、回绕解卷、局部倒退钳制、A/V 偏差超限明确失败；
  `fault.go` 带偏移/轨道号/类别的错误模型与退出码映射。
- `internal/stream`：`runner.go` 只按索引定位流式读取（载荷不整体
  入内存）；`backpressure.go` 有界队列 + context 取消 + 峰值统计；
  `resume.go` checkpoint 摘要/索引校验和/阶段不变量校验。
- `internal/remux`：`writer.go`+`track.go` 用 go-mp4 重建容器与
  stss/stco 随机访问索引，临时文件 + 原子替换提交；
  `rtp_recovery.go` 序号窗口重排与丢包恢复率；`metrics.go` JSON
  摘要（sync_error_ms、p95_latency_ms、peak_buffer_bytes 等）。
- `internal/samples`：确定性样例生成器（正常 + 未知长度截断、簇边界
  截断、时间戳回绕、多轨交错乱序、索引摘要失配五类故障）。
- `internal/qcrun`：管线编排与 selfcheck 验收套件；`cmd/mediaqc`
  为薄终端壳。

## 可重复核验

同一输入重复处理产生相同的 `index_checksum`、`repairs` 计数与
`output_sha256`（selfcheck 的 `consistency:repeat` 强制验证）。
所有量化值均由自生成样例确定，不依赖外部媒体。
