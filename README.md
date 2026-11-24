# avpipe

移动弱网音视频接收内核的本地可复用样例实现：从不完整 RTP、定时元数据和 fragmented MP4 样例中恢复可用数据，按共同媒体边界生成多码率清单，并量化同步误差、丢包恢复率、P95 处理延迟和对齐错误。

全部样例由代码和确定性 seed 生成，不读取外部媒体文件，也不依赖网络服务。

## 环境

- Go 1.23+（已在本机 Go 1.27/Darwin 验证）
- 依赖固定在 `go.mod`：
  - `github.com/abema/go-mp4 v1.3.0`
  - `github.com/pion/rtp v1.8.10`

## 一键验收

```bash
bash scripts/acceptance.sh
```

脚本会执行 `go mod tidy`、`go build ./...`、`go test ./...`，构建 `bin/avpipe`，并验证正常、截断、丢包、缺索引、DAG 阻塞和参数错误等退出类别。

题目给出的端到端命令为：

```bash
go mod tidy && go build ./... && go test ./... && \
go run ./cmd/avpipe demo --seed 7 --loss 0.05 --truncate 13 --missing-index
```

该命令包含截断样例，因此程序的语义退出码是 `2`。Go 工具通过 `go run` 运行非零退出程序时，会打印 `exit status 2` 并将 `go run` 自身状态包装为 `1`；直接运行 `go build` 产出的二进制可得到约定的 `2/3/1` 退出码。

## 常用命令

```bash
# 正常样例：退出码 0，av_sync_ms <= 20，alignment_errors = 0
go run ./cmd/avpipe demo --seed 7

# 题目验收样例：5% 丢包、在绝对偏移 13 处截断 box、缺失 sidx 索引
go run ./cmd/avpipe demo \
  --seed 7 --loss 0.05 --truncate 13 --missing-index

# 耗尽 DAG 重试，验证退出码 3 和 first_blocked_dependency
go run ./cmd/avpipe demo --fail-node align-representations

# 参数错误：退出码 1
go run ./cmd/avpipe demo --loss 1.5
```

## 输出指标

终端输出包含验收要求的全部键：

```text
av_sync_ms=7.000
loss_recovery_rate=0.838710
p95_latency_ms=0.002
manifest_segments=6
alignment_errors=0
first_blocked_dependency=none
manifest_sha256=...
```

同时输出 `recovered_packets`、`unrecovered_gaps`、`index_rebuilt`、`first_unreadable_offset` 等审计字段。清单默认原子写入 `out/manifest.json`。

相同 seed 和参数会生成相同的规范化清单字节与 `manifest_sha256`；P95 是本机实际处理耗时，只用于性能统计，不进入清单摘要。

## 退出码

| 码 | 含义 |
|---:|---|
| 0 | 全部节点成功且没有未隔离损坏 |
| 1 | 参数错误 |
| 2 | 输入损坏但已安全报告/隔离 |
| 3 | 重试耗尽或依赖被阻塞/取消 |

## 包结构

- `internal/clock`：整数 tick + 显式 timescale、跨 timescale 比较和确定性换算。
- `internal/fault`：统一错误类别与首个不可读偏移。
- `internal/rtp`：基于 `pion/rtp` 类型的重复、乱序、窗口缺口和未恢复缺口处理；不伪造媒体包。
- `internal/metadata`：SRT、确定性 AVM1、ID3 PRIV、SEI 和 MP4 `emsg` 事件解析、排序与去重。
- `internal/mp4`：自生成 ftyp/moov/sidx/emsg/moof/trun/mdat 样例；边界安全扫描、索引重建和分片级隔离；使用 `go-mp4` 解包标准结构。
- `internal/align`：多表示层共同边界交集；缺边界或边界不一致会记录 alignment error 并排除错位条目。
- `internal/dag`：拓扑依赖调度、取消后状态保留、有界重试和幂等提交。
- `internal/manifest`：稳定 JSON、SHA-256 和原子文件写入。
- `internal/metrics`：P95 与验收键输出。
- `internal/scenario`：确定性样例生成和端到端装配。
- `cmd/avpipe`：终端参数与退出码入口。

## 确定性说明

- 所有时间均为 `int64`/`uint64` 整数 tick 加 `timescale`，不使用浮点做排序或对齐。
- 跨轨事件按规范化到 `1e9` timescale 的整数时间排序，并以“轨道 + 规范时间 + 内容 SHA-256”去重。
- 解析器每次切片前检查长度；截断错误携带 `first_unreadable_offset`。
- 缺失 `sidx` 时从可读 `moof/tfdt/trun` 重建索引，并记录无法确认的时间区间。
- DAG 按确定性拓扑顺序执行；重试的幂等存储保证同一键只提交一次。
