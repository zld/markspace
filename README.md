# regress-kernel

可恢复、确定性的媒体处理内核，用于直播多码率发布后的回归确认。纯本地、
无外部服务依赖：图像帧序列与 RTP 音频包全部由代码确定性生成。

## 能力

- **统一时间基准**：视频帧与 RTP 音频统一换算到纳秒时间基准（整数运算）。
- **帧校验**：帧序号、时间戳、元数据与 SHA-256 一致性；重复、缺失、越界
  序号分别计数，失败记录帧索引并使任务失败。
- **RTP 接收**：按序号重排，容忍最多 3 包乱序；丢包按策略恢复（PLC）或
  显式标记不可恢复，绝不静默跳过；支持 uint16 序号回绕。
- **音画对齐**：输出同步误差（最大绝对/均值）与参与计算的样本数。
- **可恢复阶段**：`ingest -> align -> publish -> merge`，每阶段写输入摘要、
  状态与检查点；重跑从最后一个有效检查点继续。
- **幂等发布**：稳定任务键 + 内容摘要；产物已写但状态未提交时，重跑先核对
  摘要并补记状态，不重复发布；摘要不符则隔离任务并返回非零码。
- **确定性清单**：按任务键与分片起始时刻排序，重复投递同键同摘要去重，
  冲突摘要报错；相同输入多次运行清单字节完全一致。
- **量化输出**：每码率分片数、连续性、清单 SHA-256、同步误差、丢包恢复率
  和阶段耗时 P95。

## 目录

```
internal/media/     共享时钟、帧/包/错误模型、确定性素材与故障样例
internal/ingest/    帧校验、RTP 排序与丢包恢复、音画对齐
internal/pipeline/  状态机、检查点、幂等发布、fMP4 分片、确定性清单
cmd/regress/        终端验收适配器
```

第三方依赖固定在 `go.mod` / `go.sum`：`go-mp4 v1.3.0`、`pion/rtp v1.8.11`。

## 构建与测试

```
go mod tidy
go build ./...
go test ./...
```

## 验收

```
go run ./cmd/regress -case normal -seed 17 -out /tmp/regress-a
go run ./cmd/regress -case normal -seed 17 -out /tmp/regress-b
go run ./cmd/regress -case corrupt -seed 17
go run ./cmd/regress -case crash-after-publish -seed 17 -out /tmp/regress-recover
```

判定标准：

- 两次 `normal` 的清单 SHA-256 相同；`sync_error_ms <= 20`、
  `recovery_rate >= 0.95`、`p95_latency_ms > 0`，并列出分片数与连续性。
- `corrupt` 报告坏帧索引并以退出码 2 结束（`go run` 会包装为 1，用
  `go build` 后的二进制可观察到真实退出码 2）。
- `crash-after-publish` 首次模拟提交前退出；再次运行恢复后产物数不增加、
  清单摘要稳定，退出码 0。

或直接执行 `./scripts/accept.sh`。

## 安全与边界

- 单任务输入字节数与包数有上限，超限返回 `ErrLimitExceeded`。
- 所有产物路径经工作区根校验与键名净化，拒绝越界写入（`ErrPathEscape`）。
- 状态与分片均以临时文件 + 原子 rename 落盘，崩溃不会留下半写文件。
- 仅处理生成的图像帧序列与合成 RTP 音频，不连接真实直播源。
