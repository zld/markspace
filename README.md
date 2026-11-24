# Media Archiver Kernel

纯本地、无外部服务依赖的 PCM/WAV 多媒体归档修复与随机访问索引内核。模块使用 Go 1.23，依赖固定为：

- `github.com/abema/go-mp4 v1.4.0`：归档清单的 ISOBMFF/MP4 box 封装与回读解析。
- `github.com/pion/rtp v1.8.11`：可复用 RTP 帧时间戳扩展适配。

## 子系统

- `internal/media`：采样帧/纳秒时间基准、共享模型、携带原始字节偏移的错误。
- `internal/wav`：RIFF 块遍历、PCM 格式校验、`tms ` 时间戳块解析和尾部坏帧定位。
- `internal/repair`：缺失/重复/倒退/超容差时间戳策略修复、帧边界检查、输入摘要与决策溯源。
- `internal/index`：确定性帧/时间索引构建与二分定位。
- `internal/verify`：目标/实际码率、峰值、削波和坏块统计。
- `internal/pipeline`：解析、变换、索引、验证四阶段，清单持久化、断点续跑和幂等提交。
- `internal/samplegen`：固定 seed 的正常、截断、时间戳倒退和重复块样例生成。
- `internal/archive`、`internal/rtpx`：go-mp4 与 pion/rtp 适配器。
- `cmd/archiver`：唯一终端验收适配器。

## 常用命令

```sh
brew install go
go mod tidy
go build ./...
go test ./...

go run ./cmd/archiver demo --seed 17 --workdir ./tmp/demo
go run ./cmd/archiver demo --seed 17 --workdir ./tmp/demo --inject truncate
go run ./cmd/archiver demo --seed 17 --workdir ./tmp/recover --interrupt-after index
go run ./cmd/archiver resume --workdir ./tmp/recover
```

其他故障样例：

```sh
go run ./cmd/archiver demo --seed 17 --workdir ./tmp/backward --inject backward
go run ./cmd/archiver demo --seed 17 --workdir ./tmp/duplicate --inject duplicate
go run ./cmd/archiver demo --seed 17 --workdir ./tmp/bad-index --inject bad-index
```

重新生成提交在 `testdata/fixtures/` 中的夹具：

```sh
./scripts/generate_fixtures.sh
```

## 输出与产物

标准输出使用稳定的 `key=value` 行，包含：

```text
target_bitrate_bps
actual_bitrate_bps
bitrate_deviation_pct
frames
channels
peak
clipped_samples
bad_blocks
manifest_alignment_ns
index_entries
index_sha256
manifest_sha256
```

倒退样例还输出 `repair_count`、每个决策的原值/修复值，以及 `lookup_block`、
`lookup_frame_offset`、`expected_block`、`expected_frame_offset`。

工作目录中的主要产物：

```text
input/sample.wav
expected.json
parse/parse-report.json
transform/canonical-blocks.json
transform/canonical.pcm
transform/provenance.json
index/index.json
index/index.sha256
verify/stats.json
manifest.json
manifest.sha256
archive/manifest.mp4
```

所有 JSON 均使用结构体字段顺序和两格缩进，不写入墙钟时间或随机 map 顺序，因此相同输入与配置重复提交会得到字节一致的 `index.json` 和 `manifest.json`。

## 退出码

编译后的 `archiver` 二进制遵循：

- `0`：处理和验证通过。
- `2`：输入损坏或配置不合法。
- `3`：达到可恢复中断点，续跑状态已保留。
- `1`：其他内部失败。

终端报告会额外打印 `kernel_exit_code=<n>`。注意：Go 工具自身的 `go run` 会把子进程的非零退出统一包装为 shell 退出码 `1`，同时打印 `exit status <n>`；直接运行 `go build -o archiver ./cmd/archiver && ./archiver ...` 时保留上述精确退出码。

## 支持的整数 PCM 范围

- 采样率：8000、16000、22050、32000、44100、48000、96000 Hz。
- 声道数：1、2、4、6、8。
- 位深：8、16、24、32-bit 整数 PCM。
- `block align` 与 `byte rate` 必须和上述参数一致。

不支持未知编码；不完整尾部帧只记录原始偏移和坏块，不补零、不静默修复音频内容。
