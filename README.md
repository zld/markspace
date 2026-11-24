# MediaPack

一个纯本地、可恢复的多轨媒体解复用、显式映射、有理数时间线剪辑与无损 MP4 转封装内核。所有样例媒体和故障样例都由 Go 代码生成，不读取仓库外媒体文件。

## 验收命令

```bash
brew install go          # 若尚未安装 Go
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediapack demo --out ./tmp/demo --chunk-samples 8
go run ./cmd/mediapack verify --dir ./tmp/demo
```

`demo` 会生成：

- 正常多轨 `normal.mp4`（H.264 风格视频轨 + AAC 风格音频轨）
- QuickTime `normal.mov`
- 跨越 32 位 RTP 时间戳回绕边界的 `rtp_wrap.rtp`，其中含一个重复包
- 含缺包间隙的 `rtp_gap.rtp`
- box 越界的 `truncated_box.mp4`
- 样本 CRC 损坏的 `corrupt_sample.mp4`
- 样本声明长度越过 EOF 的 `truncated_sample.mp4`

随后执行首次分块处理、删除最后一个不可变检查点、再恢复重跑。最终 `verify` 输出：

```json
{
  "tracks": 4,
  "samples": 104,
  "payload_hash_mismatches": 0,
  "target_bitrate_bps": 208000,
  "actual_bitrate_bps": 207500,
  "bitrate_deviation_pct": 0.24...,
  "quality_dropped_samples": 0,
  "manifest_alignment_error_ms": 0,
  "recovered_chunks": 1
}
```

退出码：

- `0`：成功/校验通过
- `2`：命令参数、路径逃逸或轨道映射错误
- `3`：无法恢复的输入或清单/输入缺失
- `1`：校验数值或哈希不通过

## 断点恢复

索引目录为 `<out>/index/`：

- `chunks/*.tmp`：临时块记录
- `chunks/*.json`：校验后落地的块记录
- `checkpoints/000001.json ...`：不可变检查点
- `checkpoint.json`：最新检查点原子指针

删除最后一个检查点后：

```bash
rm tmp/demo/index/checkpoints/000014.json
go run ./cmd/mediapack recover --dir ./tmp/demo
```

Store 会回退到最近的有效检查点，只重放未提交块；检查点中的样本/字节计数不会重复累加。重复恢复运行后 `manifest.json` 的 SHA-256 保持一致。

也可直接运行：

```bash
./scripts/acceptance.sh
```

## 模块边界

- `internal/clock`：有理数时基、tick 转换和对齐误差
- `internal/fault`：统一错误类型、输入偏移、轨道/SSRC、可恢复状态和退出码
- `internal/probe`：MP4/MOV ftyp 与长度前缀 RTP 识别
- `internal/demux`：go-mp4 元数据解析、样本偏移/哈希、RTP 去重/排序/回绕/间隙
- `internal/maptrack`：一对一显式映射和时基校验
- `internal/timeline`：半开 `[in,out)` 选样、有理数剪辑点和转场重叠校验
- `internal/remux`：压缩样本原样写入 MP4，写前逐样本哈希校验，并用 go-mp4 回读探测
- `internal/index`：临时记录、校验、原子检查点和回退
- `internal/batch`：分块调度、恢复和清单生成
- `internal/metrics`：从实际输出媒体时间计算码率、偏差和对齐误差

## 依赖说明

`go.mod` 按任务要求保留 `github.com/pion/rtp/v2 v2.0.1`。截至当前公共 Go proxy / GitHub tag，`v2` 模块实际发布到 `v2.0.0`，因此 `go.mod` 用一条固定 `replace` 将请求的 v2 API 路径锁定到可用的 `v2.0.0`；`github.com/abema/go-mp4` 固定为 `v1.4.1`。
