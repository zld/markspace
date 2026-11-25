# packcheck

媒体处理内核：把同一批字幕与媒体时间码打成适用于不同平台（low/mid/high 表示层）
的资源，校验 SRT/WebVTT、多制式时间码、MP4 容器轨道与 RTP 分片边界，并在发布
失败时生成分层恢复结论与确定性的回滚清单。纯本地、无外部服务、样例数据随代码生成。

## 技术栈

- Go 1.23（本机工具链更高亦可，`go.mod` 固定 `go 1.23`）
- `github.com/abema/go-mp4 v1.3.0`：MP4 容器与轨道约束解析
- `github.com/pion/rtp v1.8.9`：RTP 时间戳与分片边界校验
- 标准库 `testing`：模块测试与故障夹具

## 构建与测试

```sh
go mod tidy
go build ./...
go test ./...          # 加 -race 可跑竞态检测
```

## 验收命令

正常样例（预期 `layer_count=3`、`aligned_boundaries=true`、`threshold=pass`、`exit=0`）：

```sh
go run ./cmd/packcheck --self-generate --fps 30000/1001-df \
  --slow-layer low --queue 2 --budget-bytes 0
```

损坏样例（字幕偏移 + mid 层截断 + 切点字幕；预期阈值失败、`exit=2`、mid 不进清单）：

```sh
go run ./cmd/packcheck --self-generate --corrupt subtitle-offset \
  --truncate layer=mid --split-at-cue --budget-bytes 0
```

> 说明：`go run` 对非零退出码固定包装为 shell 的 `1` 并打印 `exit status 2`；
> 程序自身的退出码语义为 `0` 全通过、`1` 解析/运行错误、`2` 阈值失败。
> 用 `go build -o packcheck ./cmd/packcheck` 后直接执行二进制可观察到真实退出码。

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 全部通过 |
| 1  | 参数或容器等解析/运行错误 |
| 2  | 业务阈值失败（如预算偏差超 allowance） |

## 模块划分

| 包 | 职责 |
|----|------|
| `internal/clock`     | 24/25/29.97 DF·NDF/50 fps 到统一有理数时基；非法掉帧、回绕、不兼容帧率报错 |
| `internal/model`     | 带原始字节偏移、轨道、表示层、归一化区间的错误模型 |
| `internal/subtitle`  | SRT/WebVTT 流式解析；倒置拒绝，重叠/空字幕/切点字幕带偏移上报 |
| `internal/container` | MP4 轨道时基、起点、sidx 边界校验；缺轨/时基不一致/漂移分别计数，不静默修正 |
| `internal/segment`   | RTP 时间戳连续性、分片边界、跨层签名交叉核验 |
| `internal/stream`    | 有界队列扇出（满则背压、慢消费者超时隔离并记录丢弃区间）、共享时钟汇聚 |
| `internal/recovery`  | 各表示层独立恢复、恢复前后结论、确定性回滚清单 |
| `internal/report`    | 阈值、预算偏差、异常区间、逐项结论与稳定 key=value 输出 |
| `cmd/packcheck`      | 验收适配入口与自生成夹具 |

## 工程约定

- 流式缓冲与队列容量有界，输出含 `peak_queue_depth` 高水位。
- 只支持声明的字幕格式、时间码制式与 MP4 约束，不修复未知编码。
- 拒绝路径穿越输入（`--out` 含 `..` 直接报错退出 1），输出仅写入指定本地目录。
- 回滚清单只接受原始即通过容器、字幕与边界校验的完整表示层；恢复层记录结论但不入清单。
- 清单按表示层名排序并取 SHA-256，重复运行摘要完全一致。
