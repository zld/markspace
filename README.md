# archive — 历史媒资恢复管线

将长期归档的 MP4/MXF 样例素材恢复为可复核的多轨时间线与 PNG 帧序列。
纯终端、纯本地运行，无外部服务与外部解码器依赖。

## 子系统

| 流向 | 文件 | 职责 |
|---|---|---|
| 共享模型 | `internal/model/time.go` | 有理数统一时间基准（90 kHz），禁止浮点累计 |
| | `internal/model/fault.go` | 含文件偏移、阶段、轨道号的错误模型 |
| 容器解析 | `internal/parse/mp4.go` | MP4 样本表（stts/stsz/stsc/stco/stss）+ 旋转矩阵/裁剪/色彩元数据 |
| | `internal/parse/mxf.go` | 本地样例 MXF 的 KLV 结构化元数据子集 |
| 时间线变换 | `internal/timeline/align.go` | 轨道时间 → 统一刻度（RTP 同步包锚定，非零起点） |
| | `internal/timeline/compose.go` | 裁剪/旋转/冲突色彩的版本化策略 |
| 帧导出 | `internal/render/frame.go` | 离屏确定性 PNG 帧（非编解码器解码） |
| | `internal/render/hash.go` | 逐片段 SHA-256 |
| 恢复溯源 | `internal/pipeline/journal.go` | 阶段检查点（输入哈希 + 策略版本绑定） |
| | `internal/pipeline/run.go` | 解析→变换→渲染协调与断点恢复 |
| | `internal/pipeline/verify.go` | 独立验证器 |
| 入口 | `cmd/archive/main.go` | `demo` / `verify` 命令 |

## 支持范围（明确限制）

- MP4：本仓库夹具生成的盒子子集（ftyp/moov/trak/mdia/minf/stbl 及上述叶子盒，
  外加私有 `crop`/`rtps` 盒）。MXF：私有 KLV 元数据子集。**不解码任意编解码器**；
  帧是已验证样本负载的确定性可视化。
- 限制：单输入 2 GiB、64 轨、帧尺寸 ≤ 4096×4096；整数溢出与路径越界拒绝处理。

## 验收

```sh
brew install go && go mod tidy && go build ./... && go test ./...
go run ./cmd/archive demo --out ./tmp-demo --seed 17
go run ./cmd/archive verify --dir ./tmp-demo
```

演示输出含 `roundtrip_rate=1.000`、`keyframe_hit_rate=1.000`、`segments=8`、
`hash_mismatches=0`、`frames=24`、`resume_reused_stages=2`；损坏样例输出
`error_offset=<非负偏移>` 与 `stage=parse`。重跑 demo 后逐片段 SHA-256
与清单摘要保持一致。

## 退出码

- `0` 验证通过；`1` 输入或处理失败；`2` 参数错误。
- 注：`go run` 会把非零退出码包装为自身的 1（打印 `exit status N`）；
  需要精确退出码时请使用 `go build` 产物。

## 故障样例（自生成五类）

1. `truncated_stbl` — 截断样本表（stage=parse，保留偏移）
2. `oob_offset` — 越界 chunk 偏移（stage=parse，保留偏移）
3. `bad_crop` — 畸形裁剪矩形（stage=parse，保留偏移）
4. `conflict_color` — 冲突色彩标签（compose 策略 `prefer_bt709@v1` 解决并记录）
5. `overflow_ts` — 轨道时间戳溢出（stage=align，携带轨道号与时间）
