# mediacheck

可复用的本地 Matroska/WebM 容器、轨道图、分片与 RTP 元数据校验内核。它不解码音视频、不访问网络，所有样例均由 Go 代码确定性生成。

## 安装、构建与测试（macOS Terminal）

```bash
brew install go@1.23
# Homebrew 的 go@1.23 为 keg-only；若 go version 不是 1.23.x：
export PATH="/opt/homebrew/opt/go@1.23/bin:$PATH"

go mod tidy
go build ./...
go test ./...
```

依赖版本固定在 `go.mod`：

- Go 1.23
- `github.com/abema/go-mp4 v1.3.0`：辅助 MP4 box 解析与样例检查
- `github.com/pion/rtp v1.8.11`：RTP 包表示与序列化检查

## Demo

正常样例包含未知长度 Segment、未知长度 Cluster、两条交错轨道和连续 RTP 包：

```bash
go run ./cmd/mediacheck demo --seed 7
```

重复相同 seed 会得到相同排序、偏移、偏差数值和 SHA-256 摘要。

损坏/冲突样例：

```bash
go run ./cmd/mediacheck demo --case truncated-cluster
go run ./cmd/mediacheck demo --case audio-conflict
go run ./cmd/mediacheck demo --case rtp-gap
```

退出码：

- `0`：校验通过
- `1`：输入不一致、损坏或存在可恢复但仍应报告的 RTP 缺口
- `2`：命令行参数错误

参数退出码以编译后的二进制为准（`go build -o mediacheck ./cmd/mediacheck`）；`go run` 会把任意非零子程序状态包装成 go 命令自身的退出码 `1`。

## 输出字段

每条 JSON 记录都包含固定字段：

| 字段 | 含义 |
| --- | --- |
| `sample` | 样例/运行名称 |
| `track` | Matroska 轨道号；全局记录为 `0` |
| `input_offset` | EBML 元素或对应块的原始字节偏移 |
| `time_delta_ns` | 同轨道相邻块时间差，或恢复缺口/分片持续时间 |
| `byte_delta` | 相邻原始块偏移差、分片字节长度或相邻 RTP 载荷长度差 |
| `failure_offset` | 截断/冲突检测位置；无失败的映射记录为 `-1` |
| `error_code` | 稳定错误码；正常映射为 `OK` |
| `sha256` | 长度前缀化 EBML + 序列化 RTP 包的稳定摘要 |

常见 `error_code`：

- `EBML_TRUNCATED_ELEMENT`
- `EBML_ILLEGAL_LENGTH`
- `EBML_INCOMPLETE_BLOCK`
- `TIMESTAMP_WRAP_AMBIGUOUS`
- `TRACK_LAYOUT_CONFLICT`
- `TRACK_SAMPLE_RATE_CONFLICT`
- `TRACK_COLOR_CONFLICT`
- `FRAGMENT_CROSS_TRACK_COVERAGE`
- `RTP_GAP_RECOVERED`
- `RTP_SEQUENCE_DISCONTINUITY`

## 子系统

- `internal/ebml`：VINT/元素头、已知与未知长度 Segment/Cluster、原始交错块顺序和偏移。
- `internal/timeline`：显式 `TimecodeScale` 到纳秒换算、RTP 时钟换算和有界时间戳回绕展开。
- `internal/tracks`：轨道元数据提取、依赖 DFS 顺序、声道布局/声道数、采样率和色彩元数据冲突。
- `internal/fragment`：Cluster 分片首尾时间、跨轨覆盖、偏移/时间/字节偏差，并挂载 RTP 校验结果。
- `internal/rtp`：按 SSRC 校验 uint16 序号、32 位时间戳回绕和有界缺包恢复。
- `internal/report`：所有子系统共用的结构化记录、错误码、排序、摘要计数和 JSON 输出。
- `internal/sample`：无外部文件的确定性 EBML/RTP/MP4 辅助样例生成器。

## 限制

默认上限可在 `ebml.DefaultLimits()` 中查看：

- 最大 VINT 宽度：8 字节，元素 ID：4 字节
- 单元素长度：64 MiB
- 最大块数量：1,000,000
- 分片时间跨度：5 分钟
- 可唯一恢复的 RTP 序号缺口：最多 16 个包

超过上限、缺口过大或时间戳展开不唯一时，会返回结构化错误而不是猜测修复。
