# mediacore — 本地低延迟互动音视频处理内核

为移动网络下的实时协作玩法构建的可复用本地处理内核。单进程、纯本地、无外部
媒体服务：读取随代码生成的 MP4 素材与清单，修复时间戳，重建可随机访问的
索引，在明确的内存预算内流式执行，并输出可用本地数值复核的码率、质量与
对齐证据。不做任何载荷解码或重编码，也不覆盖源文件。

## 技术栈与依赖

- Go（模块声明见 `go.mod`；依赖固定版本）
- `github.com/abema/go-mp4 v1.4.1` — MP4 box 解析与写入
- `github.com/pion/rtp v1.10.6` — RTP 包头与序号处理
- 标准库 `context`、`io`、`crypto/sha256`、`testing`
- 时间运算统一使用整数刻度（统一时间轴为纳秒），不引入浮点累计误差

## 模块结构

```
cmd/mediacore        终端验收适配器（demo / 单场景 / 退出码）
internal/clock       共享整数时间基准与可分类错误（Kind -> 退出码）
internal/scan        box 边界校验、样本表游标、编辑列表映射、关键帧回退
internal/index       固定宽度二进制索引、SHA-256 摘要校验、原子缓存
internal/repair      RTP 32 位回绕、序号回绕、乱序、缺口与稳定错误
internal/stream      样本/字节双上限分块、背压信号量、可恢复检查点
internal/metrics     码率偏差、样本大小分布、对齐误差（整数统计）
internal/testkit     夹具生成、五类损坏夹具、端到端流水线与恢复测试
```

数据流：`scan`（读 MP4 元数据，按轨产出样本）→ `index`（校验输入摘要后
构建）→ `repair`（修复回绕/乱序/缺口）→ `stream`（按索引分批读取、修复并
输出）→ `metrics`（在同一时间轴上汇总证据）。

## 构建与测试

```bash
brew install go@1.23        # 或任意满足 go.mod 指令的 Go
go mod download
go mod tidy
go build ./...
go test ./...
```

## 验收演示

```bash
go run ./cmd/mediacore demo \
  --duration 3600s --target-bps 1800000 \
  --batch-bytes 1048576 --slow-every 7 --fixtures generated
```

演示自行生成 normal、truncated、edit_oob、wrap、index_broken、no_keyframe
六类样例，并对 normal 场景打印：

| 字段 | 验收要求 |
| --- | --- |
| `target_bps` | 1800000 |
| `actual_bps` | 约 1800000 |
| `bitrate_deviation_pct` | < 2.0 |
| `quality_samples` | > 10000 |
| `dropped_samples` | 0（normal） |
| `alignment_error_ms` | <= 2 |
| `index_cache` | hit 或 rebuilt |
| `peak_buffer_bytes` | <= 2097152 |

单场景模式用于观察真实进程退出码：

```bash
go run ./cmd/mediacore demo --scenario truncated  --fixtures fx   # 退出 2
go run ./cmd/mediacore demo --scenario normal     --fixtures fx   # 退出 0
go run ./cmd/mediacore demo --scenario bogus      --fixtures fx   # 退出 3
```

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 成功 |
| 2 | 可恢复损坏（截断、缺关键帧、编辑越界、不可修复回退）或取消 |
| 3 | 参数错误 |
| 4 | 内部不变量失败 |

## 关键设计与边界

- **有界内存**：扫描以表游标分批，索引为固定宽度 O(1) 记录，流执行用字节
  计数信号量限制在途缓冲；慢消费者阻塞时生产端停止预读。
- **损坏不越界**：box 长度、chunk/样本偏移、记录数、输出路径全部做边界
  校验；截断与非法长度在读取前被拒绝。
- **缓存一致性**：索引以输入字节摘要 + 轨配置 + 时间基准版本为键，摘要
  变化、截断或版本不符均重建；临时文件 + 重命名保证中断后可安全重跑。
- **可恢复检查点**：每批原子落盘，包含样本位置、累计计数与 SHA-256 中间
  状态；恢复时把输出截断到检查点偏移并续算摘要，收敛到与干净运行相同的
  输出摘要。
- **确定性**：夹具由整数种子生成，重跑相同输入产生相同修复摘要与报告；
  测试覆盖慢消费者取消与重复运行摘要一致。
- **质量口径**：仅基于可复核的样本大小分布与丢弃计数，不断言无解码情况下
  的主观画质。

性能结论仅对随代码生成的样例规模与本机环境成立，不声称实时保证。
