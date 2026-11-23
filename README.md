# archivectl — 媒资归档恢复流水线

纯本地、无图形设备依赖的 Matroska/WebM 归档恢复工具。它从含未知长度 EBML
元素、多轨交错块、截断与损坏片段的容器中恢复可用时间线，重建索引、做有界
RTP 丢包恢复，并导出可重复核对的离屏 PNG 帧与 SHA-256 清单。

## 模块结构

| 包 | 职责 |
| --- | --- |
| `internal/ebml` | 有界 EBML/VINT 读取、未知长度元素、簇与块解析、截断抢救、结构化错误 |
| `internal/timeline` | 整数纳秒统一时钟、轨道依赖图、多轨合并与单调性/去重/范围不变量 |
| `internal/recover` | 索引重建（损坏偏移单独记录）、有界确定性 RTP 重排/去重/丢包恢复、恢复顺序策略 |
| `internal/render` | 离屏确定性 PNG 帧、SHA-256 清单、go-mp4 mdat 清单盒、音频统计 |
| `internal/fixture` | 六类自生成夹具（正常交错、未知长度、尾截断、簇内损坏、不完整索引、RTP 乱序丢包） |
| `internal/pipeline` | 解析→轨道图→时间线→索引/RTP 恢复→导出 的确定性编排与跨阶段不变量 |
| `cmd/archivectl` | 终端入口 |
| `testdata/generate.go` | 带 build tag 的夹具生成入口 |

## 构建、测试与演示

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/archivectl demo --seed 42 --loss-window 8 --out /tmp/archive-demo
```

演示输出字段：`av_sync_error_ms`（正常样例 P95 ≤ 20ms）、
`packet_recovery_rate`（窗口内恢复率 ≥ 0.90）、`pipeline_p95_ms`
（100 个自生成片段 P95 ≤ 500ms）、`frames`、`index_rebuilt`、
`frame_sha256`、`error_count`。相同参数重复运行产生相同帧哈希与计数。

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 处理完成（含可分类的局部损坏） |
| 2 | 参数错误 |
| 3 | 输入不可恢复 |
| 4 | 跨阶段不变量失败 |

## 约束

仅处理自生成夹具与本地输入；单文件上限 2 GiB、轨道数 32、恢复窗口 256
包；不重新编码，不保证修复不可解码码流。
