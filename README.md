# mediad

可复用的区域直播媒体处理内核：统一有理数时间基准、RTP/MP4/EDL 接入、音频重采样/声道/响度处理、分块检查点和确定性清单。

## 验收命令

```bash
brew install go
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediad demo \
  --seed 23 --drift-ppm 120 --wrap-at 4294967000 --vfr \
  --chunk 2s --target-rate 48000 --layout stereo --loudness -16 \
  --out /tmp/mediad-demo
```

重复执行同一命令会校验并复用已提交块，标准输出中的 `output_hash` 保持不变。

故障验收：

```bash
go run ./cmd/mediad demo --seed 23 \
  --corrupt-index --truncate-chunk --late-overlap \
  --out /tmp/mediad-bad
```

该命令以非零状态退出，并分别报告索引重建、截断块重做和重叠片段拒绝。

也可以运行：

```bash
scripts/generate_samples.sh /tmp/mediad-generated
```

## 包结构

- `internal/clock`：有理数刻度、DTS/PTS 来源、32 位回绕、窗口漂移拟合、显式 VFR 边界。
- `internal/mederr`：带轨道、时间范围和错误类别的统一错误。
- `internal/ingest`：go-mp4 时间线读写、长度分帧 RTP 解析、严格 EDL 校验。
- `internal/audio`：无累计相位的区间重采样、mono/stereo/5.1 布局、K-weighting 响度和峰值控制、PCM/WAV。
- `internal/batch`：时间区间分块、临时文件、SHA-256 校验、原子索引、指纹复用和重叠拒绝。
- `internal/manifest`：逐段码率、音频参数和对齐误差清单。
- `internal/demo`：确定性样例生成与验收编排。
- `cmd/mediad`：纯终端入口。

## 依赖说明

技术说明写作 `go-mp4 v1.0.4`，但公开的 `github.com/abema/go-mp4` 没有 `v1.0.4` 标签；仓库沿用已固定且可复现的实际版本 `v1.0.0`。其余关键依赖为 Go 1.23 和 `github.com/pion/rtp v1.8.13`。
