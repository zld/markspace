# mediapipe

纯本地、可续跑的区域直播跨节点媒体处理内核。输入和测试样本均由 Go 代码生成的带分帧 CRC 的 RTP/RTCP 流，不依赖外部音视频素材或原生媒体工具。

## 环境

- macOS / Darwin
- Go 1.23+
- 依赖版本已固定在 `go.mod`：
  - `github.com/pion/rtp v1.8.11`
  - `github.com/pion/rtcp v1.2.15`
  - `github.com/abema/go-mp4 v1.4.1`

## 验收命令

```bash
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediapipe demo \
  --out ./tmp/demo \
  --target-bitrate 64000 \
  --segment-ms 2000 \
  --checkpoint-packets 128

go run ./cmd/mediapipe demo \
  --out ./tmp/damaged \
  --inject truncation,corrupt,missing-index \
  --target-bitrate 64000 \
  --segment-ms 2000 \
  --checkpoint-packets 128

go run ./cmd/mediapipe resume --out ./tmp/damaged
go run ./cmd/mediapipe verify --out ./tmp/damaged
```

也可以运行：

```bash
./scripts/generate_fixtures.sh
```

生成的文件默认位于 `tmp/fixtures/`，该目录已被 `.gitignore` 忽略。

## 终端命令

- `demo`：生成确定性 RTP/RTCP 样本流并执行完整处理。
- `resume`：校验输入 SHA-256 与检查点，从最后一个完整包块继续。
- `verify`：比较 `manifest.json`、`summary.json` 与 `index.mp4` 的片段时间和摘要。

退出码：

- `0`：成功，包括可继续处理的截断、损坏载荷和缺索引恢复。
- `1`：参数、清单/检查点等非头部错误。
- `2`：无法安全解析的输入流头部；错误中包含字节偏移。

> 使用 `go run` 直接运行且子程序以码 2 退出时，Go 工具自身可能打印 `exit status 2` 并返回 1；编译后的 `./mediapipe` 二进制会精确返回 2。CLI 测试直接验证 `run(...)` 的退出码。

## 输出文件

输出目录包含：

- `input.mps`：长度分帧 + CRC32 的 RTP/RTCP 样本流。
- `manifest.json`：完整片段、指纹、去重来源映射和验收指标。
- `summary.json`：终端摘要的稳定 JSON。
- `index.mp4`：使用 `go-mp4` 写入 free box JSON 元数据的可恢复片段索引。
- `checkpoint.json`：最近完整包块检查点，临时文件写入后原子 rename。
- `checkpoint.prev.json`：上一有效检查点，当前检查点损坏时回退。

## 处理管线

```text
framed RTP/RTCP
  -> header/CRC/truncation reader
  -> 16/32 bit wrap expansion + bounded reorder/duplicate/late window
  -> RTCP Sender Report mapping + monotonic media timeline
  -> nearest-packet segment cutter
  -> fixed-window RMS silence classification
  -> fixed-window content fingerprints
  -> cross-segment dedupe with merged -> kept source mapping
  -> block checkpoint + atomic MP4 index
  -> deterministic JSON manifest
```

## 关键语义

- RTP 序号按 16 位最短有符号回绕展开；RTP 时间戳按 32 位回绕展开。
- 重排窗口内恢复乱序；窗口外到达的旧包计为 `packet_late` 并丢弃；重复包只计数，不重复进入内容。
- RTCP SR 建立 RTP 时间到媒体时间的映射；损坏或缺失 SR 时沿用最近有效映射，并记录 RTCP 质量统计。媒体时间有单调钳制，不会倒退。
- 切片目标时间选择最近的可用 RTP 包边界；截断尾片保留数据并标记 `complete=false`、`recoverable=true`。
- 静音窗口使用固定 20ms 窗口和可配置 RMS 阈值。损坏或覆盖不完整的窗口记为 `unknown`，不会误报为静音。
- 指纹使用固定 200ms 内容窗口；片段全部完整窗口哈希一致且相对时间误差在容差内才去重，并保留 `merged_segment -> kept_segment` 映射。
- 检查点保存输入摘要、已处理包数、片段、指纹、去重映射和计数。恢复时重新解析本地输入以重建有界重排/切片内部状态，但只追加检查点之后的内容结果，因此已提交块不会重复计数。
- 相同输入和参数生成相同的 `Summary.Digest`。

## 包结构

| 路径 | 职责 |
| --- | --- |
| `internal/model` | 共享媒体模型与结构化错误分类 |
| `internal/clock` | 回绕展开、RTCP/NTP 映射、单调媒体时间 |
| `internal/stream` | 分帧读取、截断/损坏识别、重排、重复/迟到/丢包统计 |
| `internal/segment` | 最近包边界切片、码率、MP4 索引和原子文件写入 |
| `internal/audio` | 可解析 PCM 样本载荷、RMS 静音/未知窗口 |
| `internal/fingerprint` | 时间窗指纹与跨片段来源映射 |
| `internal/sample` | 纯 Go 确定性 RTP/RTCP/损坏样本生成 |
| `internal/pipeline` | 编排、检查点、续跑和摘要一致性 |
| `internal/report` | JSON 清单、终端摘要和 verify |
| `cmd/mediapipe` | 终端命令与退出码适配 |
