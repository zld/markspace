# mediad — 可追溯、可重复核验的媒体处理内核

面向移动网络低延迟互动场景：在多码率 DASH 表示层、带 discontinuity 的 HLS
播放列表、容器/RTP 时间戳混杂的输入下，产出确定性的切片、静音区间与去重结果，
并把每一步的来源、解析器版本和检查点写入本地文件，使中断后可续跑、结果可复核。

纯 Go 1.23 实现，无外部服务依赖；所有样例数据由代码生成。

## 依赖（go.mod 固定版本）

- `github.com/abema/go-mp4 v1.4.1` — MP4 box 解析
- `github.com/pion/rtp v1.8.11` — RTP 包头与时间戳解析
- 测试仅用标准库 `testing`

## 模块与数据流

```
manifest (DASH/HLS) ──┐
                      ├─> core timeline ─> split ─> silence ─> fingerprint/dedupe
adapter (mp4/rtp)  ───┘        │                              │
                               └──────── journal + provenance ┘
```

- `internal/model` — 统一 timescale / 纳秒单调播放时间、有界整数运算、带来源位置的错误模型
- `internal/manifest` — DASH MPD 与 HLS 播放列表解析，20ms 表示层对齐校验，往返一致性
- `internal/adapter` — 版本化解析器注册表；`mp4`、`rtp` 两个 v1 适配器，仅支持已注册格式
- `internal/core` — 时间轴装配、关键帧对齐切片、静音检测、确定性指纹去重
- `internal/journal` — 按「清单摘要 + 解析器版本 + 处理参数」区分的原子检查点
- `internal/provenance` — 不可变来源记录，把每个输出 chunk 链回清单条目与源分片
- `internal/fixture` — 全部夹具的进程内生成器
- `internal/pipeline` — 可续跑的端到端编排
- `cmd/mediad` — 终端入口

## 本地运行（macOS）

```bash
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediad fixture --out .tmp/sample
go run ./cmd/mediad process --input .tmp/sample --state .tmp/state --json
```

夹具包含五个用例：正常 DASH、DASH 零 timescale、HLS discontinuity、
截断/损坏 box、RTP 时间戳回退。

### 输出字段

`roundtrip_consistency`、`keyframe_hit_rate`、`segment_hashes`、`silence_count`、
`deduplicated_count`、`errors`、`resumed_count`，每项还带逐用例明细。

正常样例的往返一致率与关键帧命中率均为 `1.0`；损坏项只产生带来源位置的错误，
不生成任何索引。

### 续跑与确定性

以相同参数再次执行 `process`：已提交项被跳过，`resumed_count` 等于已完成项数，
`segment_hashes` 与首次逐项一致。失败项不提交、保持可重试，且永远不会覆盖
已提交结果。检查点写入采用临时文件 + 原子 rename。

### 退出码

| 码 | 含义 |
|----|------|
| 0  | 全部成功（含 `--only` 过滤后的健康子集） |
| 2  | 参数错误或致命输入错误（如缺少 cases.json / 清单） |
| 3  | 一个或多个用例处理失败；仍输出完整逐项报告与错误数 |

## 工程约定

- 处理只读输入，结果与检查点只写入指定目录
- 相同输入、解析器版本与参数下产出确定性结果
- 时间戳/偏移使用有界整数运算，超范围字段被拒绝而非截断
- 不承诺解码任意编解码器；仅调用注册表中明确注册的解析器
- 时间轴无法可靠恢复（回退、不可恢复回绕）时报错，不伪造索引
