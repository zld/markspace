# media-pack / 可复用媒体处理内核

面向游戏发布素材的本地、确定性媒体处理内核：整数时基、SRT/WebVTT、摘要索引、有界多码率扇出、共享时钟扇入、MP4 边界解析、检查点恢复/回滚和 JSON 指标报告。

- Go 1.23
- `github.com/abema/go-mp4 v1.3.0`
- `github.com/pion/rtp v1.8.9`
- 纯本地运行，不访问网络或外部样例文件；所有夹具由代码生成。

## macOS 验收

```bash
brew install go
go mod tidy
go build ./...
go test ./...

go run ./cmd/pack demo --out /tmp/game-pack
# 再次执行：rebuilt_assets=0，segment_sha256 与第一次相同
go run ./cmd/pack demo --out /tmp/game-pack

go run ./cmd/pack demo --corrupt-boundary --out /tmp/game-pack-bad
go run ./cmd/pack demo --resume --out /tmp/game-pack-bad
```

也可以运行完整脚本：

```bash
./scripts/setup-macos.sh   # 可选：brew install go + go mod tidy
./scripts/acceptance.sh
```

### 退出码说明

`go run` 按 Go 工具自身约定不会透传子程序退出码 2，而是向 stderr 打印
`exit status 2` 并让 `go run` 包装命令返回 1；JSON 中的
`failure.exit_code` 固定为语义退出码 2。需要由 shell/CI 直接检查进程码时，
请使用构建后的二进制：

```bash
go build -o /tmp/pack ./cmd/pack
/tmp/pack demo --corrupt-boundary --out /tmp/game-pack-bad
echo $?   # 2
```

## 正常 JSON 字段

成功报告包含验收要求的全部字段：

```json
{
  "input_sha256": "64 位十六进制源摘要",
  "frame_sha256": "64 位十六进制离屏帧摘要",
  "reused_assets": 11,
  "rebuilt_assets": 0,
  "segment_sha256": "按稳定顺序汇总的 64 位十六进制片段摘要",
  "segment_hashes": ["每个片段的 64 位十六进制摘要"],
  "pixel_mean": 127.5,
  "peak_dbfs": -3.098,
  "alignment_error_ms": 0,
  "frame_duration_ms": 40,
  "boundary_verification": { "passed": true }
}
```

约束：

- 所有 SHA-256 均为小写 64 位十六进制。
- `pixel_mean` 在 `0..255`。
- `peak_dbfs` 为 dBFS；数字静音报告有限值 `-96`。
- 正常 `alignment_error_ms <= frame_duration_ms`（25fps 输出为 40ms）。
- 损坏样例在 `ios/720p` 第 2 个片段注入 3 帧（120ms）偏移，错误定位包含
  `layer`、`segment=2`、`offset`（共享时基 tick）和 `offset_ms=120`。

## 输出目录

```text
<out>/
  index.json                         # 内容/参数/平台摘要索引
  release-manifest.json              # 当前发布报告副本
  subtitles/game-dialogue.srt|.vtt
  mp4/<platform>/<rendition>/segment-001.mp4 ...
  work/checkpoint.json               # 各层最近完整边界
  published/current.json             # 当前已发布清单
  published/previous.json            # 上一版清单（可回滚）
  published/versions/release-*.json
```

回滚：

```bash
go run ./cmd/pack rollback --out /tmp/game-pack
```

## 关键语义

1. **共享整数时基**：`clock.TicksPerSecond=1,800,000`，可整除 24/25/50fps
   与 29.97（`30000/1001`）帧时长。非法 DF 标记、负时间、越界时间均返回
   带来源和偏移的 `fault.Error`，不静默修正。
2. **字幕**：保留原始字节 SHA-256 与 cue 的半开 `[Start,End)` 边界；跨片段
   cue 可切分并无损合并。零时长和重叠 cue 有独立错误类别。
3. **摘要索引**：键由 `source_sha256 + params_sha256 + platform` 构成。
   只有键变化或记录缺失的资产进入 `rebuilt_assets`。
4. **扇出**：每层一个有界 channel；满队列为源端提供背压，不丢边界。
   单层取消/损坏只标记该层，其他层继续。
5. **扇入**：用共享时钟和一帧容差核对片段起止；pion/rtp 检查重复序号和
   时间戳回退；缺片、重复序号、回退和边界误差均定位层、片段、偏移。
6. **恢复**：检查点只在 MP4 解析和窗口边界校验成功后按连续片段提交。
   `--resume` 从每层 `last_complete_index+1` 开始；身份摘要不匹配返回
   `digest_mismatch`，且不覆盖当前已发布版本。
7. **发布**：成功后原子写清单；验证失败返回退出码 2，不调用发布替换。

## 测试映射

```text
非法 DF：             internal/clock/timebase_test.go
字幕切点：            internal/subtitle/subtitle_test.go
慢消费者背压：        internal/stream/stream_test.go
重复 RTP 序号：       internal/stream/stream_test.go
摘要变化：            internal/index/store_test.go
中断恢复/回滚：       internal/recovery/checkpoint_test.go
端到端损坏与续做：    internal/pipeline/demo_test.go
```

运行详细测试名：

```bash
go test -v ./...
```
