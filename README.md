# archive-recovery

历史影视素材码流恢复与溯源工具。从旧剪辑系统 / 专用归档设备导出的 H.264、
H.265、AV1 裸码流中重建随机访问索引、修复缺失/异常时间戳，并对每一个恢复结论
保留可审计的字节级溯源证据。纯本地运行，无外部服务或系统级编解码器依赖。

## 构建与验收

```sh
go mod tidy
go build ./...
go test ./...

# 受控中断后续跑（所有产物位于临时目录）
go run ./cmd/archivectl demo   --codec av1 --seed 42 --chunk-bytes 4096 --interrupt-after 2
go run ./cmd/archivectl demo   --codec av1 --seed 42 --chunk-bytes 4096 --resume
go run ./cmd/archivectl verify --codec av1 --seed 42
```

终端字段：`roundtrip_match_pct`、`keyframe_hit_pct`、`chunks`、
`sha256_match=N/N`、`repaired_timestamps`、`truncated_units`、
`resume_skipped_chunks`、`manifest_sha256`（64 位十六进制）。

退出码：

| 码 | 含义 |
|----|------|
| 0  | 完成且校验通过 |
| 2  | 输入或参数无效（含未知适配器版本，绝不静默降级） |
| 3  | 可恢复解析错误（截断 NAL/OBU 等，损坏区不进索引） |
| 4  | 检查点或结果校验失败 |

## 模块布局

| 包 | 职责 |
|----|------|
| `internal/media`       | 有理数时间基准 `Clock`、归一化 `FrameEvent`、分层错误模型 |
| `internal/adapter`     | 版本化适配器注册；H.264/H.265 Annex B 与 AV1 OBU 流式解析 |
| `internal/repair`      | 缺失/重复/倒退/33 位回绕时间戳的单调修复与原因留痕 |
| `internal/index`       | 旧索引读取校验、失效区段重建、确定性随机访问索引 |
| `internal/provenance`  | 定长分片 SHA-256、延迟确认/污染标记、确定性溯源清单 |
| `internal/job`         | 检查点续跑、幂等编排、输出路径隔离、端到端往返校验 |
| `internal/transport`   | pion/rtp 33 位时间戳承载、go-mp4 清单容器封装 |
| `internal/testkit`     | 由种子生成的正常/截断/坏索引/回绕/边界相邻夹具 |
| `cmd/archivectl`       | 终端验收入口 |

## 关键不变量

- 内存随 `--chunk-bytes` 有界；解析器拒绝超长长度字段（LEB128 非规范/第 9 字节、
  NAL/OBU 超尺寸上限）与整数溢出。
- 截断单元只记录偏移与错误类别，绝不写入索引；其覆盖的分片被永久标记为污染，
  不能被后续单元“洗白”。
- 检查点绑定输入哈希、配置摘要与适配器版本；任一变化即拒绝沿用，且保留现场。
- 清单为确定性 JSON，重复运行产生相同 `manifest_sha256`；输出目录不得覆盖输入。
