# mediatrace

离线重建可复核的媒体轨迹，并把规格不一的音轨统一交付为标准化 AAC。
纯终端、纯本地运行，无外部服务依赖。

## 子系统

| 包 | 职责 |
| --- | --- |
| `internal/ebml` | 流式扫描未知长度 Segment/Cluster 与交错块；块偏移、轨道、时间戳建索引；`at-wat/ebml-go` 交叉校验；带 SHA-256 尾部的持久化索引 |
| `internal/timeline` | Matroska 时间码与 RTP（含 32 位回绕）统一换算为纳秒；JSONL 事件加载；块-事件最近邻对齐，倒退/缺口显式标注而不猜时间 |
| `internal/audio` | FFmpeg 两遍 loudnorm（目标 -16 LUFS，真峰值 ≤ -1 dBTP）；ebur128 独立测量；质量门限判定 |
| `internal/cache` | 内容寻址索引，`hit`/`miss`/`rebuilt` 三态；临时索引中断后不可复用 |
| `internal/output` | `go-mp4` 独立核验样本表；确定性清单；staging 目录单次 rename 原子发布 |
| `internal/engine` | 扫描→缓存→对齐→每轨三档码率→门限选最小合格者→清单→发布 |
| `internal/fixture` | 自包含确定性 WebM/事件夹具生成（两轨四块、截断、低响度变体） |
| `cmd/trace` | `demo` / `run` / `verify` 子命令 |

## 构建与测试

```sh
brew install go ffmpeg
go install golang.org/dl/go1.23.12@latest && go1.23.12 download
go1.23.12 mod tidy
go1.23.12 build -o /tmp/trace ./cmd/trace
go1.23.12 test ./...
```

## 运行

```sh
/tmp/trace demo --dir /tmp/trace-fixtures
/tmp/trace run -i /tmp/trace-fixtures/ok.webm \
  -e /tmp/trace-fixtures/events.jsonl \
  -c /tmp/trace-fixtures/cache -o /tmp/trace-out --budget-passes 3
/tmp/trace verify -o /tmp/trace-out
```

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 成功 |
| 2 | 输入损坏（稳定错误码 + 首个字节偏移） |
| 3 | 质量/预算不满足（无合格 AAC 候选，不降门限） |
| 4 | 核验或写出失败 |

## 设计约束

- 先扫描建索引，再按索引流式读取；索引条目按需 `ReadAt`，内存不随文件增长。
- 输入 SHA-256 变化或索引损坏即重建；修复输入在同一调查槽位上显示 `cache=rebuilt`。
- 质量优先：三档码率（128/96/64k）全部测量后，仅在合格候选中选体积最小者。
- 损坏输入不发布任何半成品；重复处理同一输入产生相同内容摘要。
- 仅限本地可解码音轨，不改写视频；输入大小、轨道数、块数与解码时长均有上限。
