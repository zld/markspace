# archivecheck

可复用的 Go 归档核对内核：从 DASH/HLS 清单、RTP/JSONL 事件和本地自生成 fMP4 夹具重建时间线与处理状态，输出可核对的同步、丢包、恢复、重复、缓存、检查点和延迟指标。

## 验收命令

在项目目录执行：

```bash
brew install go@1.23
export PATH="$(brew --prefix go@1.23)/bin:$PATH"

go version
go mod tidy
go mod vendor
go build -o /tmp/archivecheck ./cmd/archivecheck
go test ./...

go run ./cmd/archivecheck demo \
  --case all --seed 17 --queue 2 \
  --memory 67108864 --slow-ms 2
```

也可以运行：

```bash
bash scripts/acceptance.sh
bash scripts/demo.sh
```

退出码：

- `0`：样例处理和指标断言全部完成；
- `1`：内部错误或验收指标不符；
- `2`：命令行参数错误。

## 演示输出

`demo --case all` 逐行输出 normal、corrupt、recovery 三个 JSON 对象。固定种子 `17` 下：

- normal：`sync_ms <= 20`、`loss=4`、`recovered=4`、`recover_rate="100%"`、`duplicate=2`；
- corrupt：`bad_segments=2`、`fallback=1`、`unresolved=1`；
- recovery：`resumed=1`，且 `archive_sha256` 与连续运行的 `continuous_sha256` 相同；
- 每行均包含 `p95_ms <= 1000` 和 `inflight_peak_bytes <= --memory`。

演示文件默认写入当前项目下的 `.archivecheck-work/`，不会访问网络或外部服务。

## 包结构

- `internal/timebase.go`：统一整数纳秒时基、十进制秒解析、timescale 换算和溢出检查。
- `internal/fault.go`：带阶段、来源和偏移的错误模型。
- `manifest/dash.go`：DASH 多表示层、`SegmentTemplate` 和 `SegmentTimeline` 展开。
- `manifest/hls.go`：EXTINF 与 `EXT-X-DISCONTINUITY` 解析。
- `manifest/fmp4.go`：使用 `github.com/abema/go-mp4/v1.7.3` 校验 fMP4 分片。
- `rtp/ingest.go`：JSONL 事件、`pion/rtp/v1.8.27` 报文解析、序号回绕、乱序、重复和缺口恢复。
- `cache/cas.go`：SHA-256 内容寻址缓存；命中后仍重读来源并核对来源标识与摘要。
- `pipe/stream.go`：固定容量队列、背压、取消游标、内存预算、峰值在途字节和 P95。
- `checkpoint/journal.go`：阶段 JSONL 日志和原子替换的检查点。
- `state/replay.go`、`state/run.go`：状态重建、续跑、隔离/回退和归档摘要。
- `metrics/report.go`：JSONL 指标和音画 PTS 对齐。
- `fixture/`：由 Go 代码生成正常、损坏和中断恢复夹具。
- `cmd/archivecheck/`：纯终端入口。

## 作为 Go 包使用

核心包均可独立导入，例如：

```go
tr := rtp.NewTracker()
obs, err := tr.Observe(event)

cas, _ := cache.New("/path/to/cas")
res, data, err := cas.Ensure(ctx, sourceID, claimedSHA256, readFromSource)

st := pipe.NewStream(pipe.Config{Queue: 8, MemoryBytes: 64 << 20}, handler)
st.Start()
_ = st.Send(ctx, pipe.Record{Cursor: 1, Source: sourceID})
_ = st.Close()
```

各核心包均有独立测试；`go test ./...` 会覆盖时间基溢出、清单错误位置、RTP 回绕/缺口、缓存不符重接入、背压/取消、检查点重放、损坏隔离、回退和中断后归档一致性。
