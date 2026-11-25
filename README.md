# rtptrace

可复用的 Go 弱网 RTP 离线回归内核。项目使用固定版本的 `github.com/abema/go-mp4 v1.3.0`
读取生成器构造的 MP4 轨道、编辑列表和样本表，使用 `github.com/pion/rtp v1.8.11`
解析/生成 RTP 包，并输出确定性的分片、多码率表示层、逐样本映射、偏差、失败位置和
SHA-256 稳定摘要。

所有素材均由 `internal/fixture` 按 seed 生成，不读取外部媒体、RTP 抓包或网络服务。

## 验收命令

在 macOS Terminal 中进入项目目录：

```bash
brew install go
go mod tidy
go build ./...
go test ./...

go run ./cmd/rtptrace demo \
  --seed 17 --duration 120s --segment 6s \
  --bitrates 800k,1600k --report json

go run ./cmd/rtptrace demo \
  --seed 17 --fault truncated-stbl --report json

go run ./cmd/rtptrace demo \
  --seed 17 --fault edit-out-of-range --recover --report json
```

也可以运行完整脚本：

```bash
./scripts/acceptance.sh
```

为获得规格中定义的精确进程退出码，请直接运行编译产物：

```bash
go build -o ./bin/rtptrace ./cmd/rtptrace
./bin/rtptrace demo ...
echo $?   # 0=成功；2=输入损坏但已生成可用报告；64=参数错误；1=内部错误
```

> `go run` 会把子进程的非零退出码统一包装成 go 工具自身的 `1`，并在 stderr 打印
> `exit status 2`；编译后的二进制保留定义的退出码 `2`。

## 确定性 JSON 契约

报告顶层和 `mappings[]` 每一行都包含：

- `input_sample`
- `track_id`
- `presentation_start`
- `segment_id`
- `representation`
- `time_error_ticks`
- `byte_error`
- `failure_track`
- `failure_sample`
- `recovery_state`
- `summary_sha256`

正常 seed 17、120 秒、6 秒分片、`800k,1600k` 输出 720 条逐样本×表示层映射；
`time_error_ticks` 和 `byte_error` 均为 0。RTP 计数包含：

- `loss_count` / `rtp.counts.lost`
- `duplicate_count`
- `out_of_order_count`
- `late_count`
- `rtp.counts.sequence_wraps`

`summary_sha256` 是 64 个十六进制字符。摘要只覆盖规范化后的报告字段，不包含时间戳、
主机路径或 Go map 迭代顺序。相同命令重复执行产生字节级相同 JSON。

## 模块

| 路径 | 职责 |
| --- | --- |
| `internal/fixture` | 确定性清单、MP4 box 子集和弱网 RTP 轨迹生成 |
| `internal/manifest` | JSON 清单校验、`go-mp4` 轨道/edts/stts/stsz/stss 读取 |
| `internal/timeline` | 有理数时基、余数保留、媒体时间与呈现时间编辑映射 |
| `internal/track` | 依赖图、环检测、失败传播、音频矩阵/重采样和色彩元数据保留 |
| `internal/rtp` | pion RTP 解码、16 位序号回绕、丢包/重复/乱序/窗口外迟到分类 |
| `internal/segment` | 不晚于目标边界的关键帧锚点、多码率对齐、逐样本映射、分片缓存释放 |
| `internal/recover` | 截断样本表、越界编辑、缺失关键帧等确定性恢复/失败状态 |
| `internal/evidence` | 扁平证据行、稳定规范化摘要和 JSON 输出 |
| `internal/pipeline` | 串联完整数据流和退出分类 |
| `cmd/rtptrace` | 纯终端参数入口 |

## 关键不变量

- 时间始终以有理数传递；刻度换算保留商和余数，不逐样本累计取整。
- 目标分片边界不会因缺少关键帧而静默移动；失败会给出轨道和源样本位置。
- 空编辑映射为时间线间隙，不会被删除或折叠。
- 依赖环报告轨道 ID 和依赖边；依赖失败时 `publish_allowed=false`。
- 不支持的声道布局或采样率在具体轨道失败，不猜测转换。
- 缺失或冲突色彩标签只保留源值、替代值和选择依据，不改写源标签。
- 重复 RTP 包只计数；窗口外迟到包形成局部失败，不阻止后续分片处理。
