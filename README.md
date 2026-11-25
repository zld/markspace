# MediaCore

可复用的纯本地 Go 媒体处理内核，用于长期媒资归档/恢复验收。它从 MP4/MOV 与代码生成的 RTP 夹具建立带 SHA-256 摘要的索引，完成 RTP 去重/重排/缺口记录、轨道映射、无损样本复制和 MP4 转封装，并输出 JSON 清单与终端数值指标。

## 运行

```bash
brew install go
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediacore selftest --generate --work ./tmp --report text
```

也可以执行：

```bash
./scripts/selftest.sh
```

验收成功时退出码为 `0`；处理或核验失败时为非零。`--report json` 可输出完整 JSON 报告，`verify-manifest --manifest <path>` 可单独复核清单。

## 终端指标

自检生成并处理：

- `inputs/normal.mp4`：双轨、2 秒、精确目标码率样本；
- `inputs/truncated.mp4`：在 `mdat` 头后截断，返回 `truncated_data` 和文件偏移 `24`；
- `inputs/lossy-reordered-duplicate.rtpbin`：两个 SSRC、逆序到达、4 个重复包、两个突发缺口共 10 个丢包；
- `inputs/changed-v1.mp4` 与 `inputs/changed-v2.mp4`：摘要变化后旧索引被拒绝并重建。

关键输出包括：

```text
target_bitrate=5376000
actual_bitrate=...
bitrate_delta_pct<=1
video_psnr=+Inf/exact
audio_diff_samples=0
manifest_alignment_ms<=1
duplicates_removed=4
reordered_packets=62
missing_packets=10
index_rebuilt=1
normal_missing_packets=0
lossy_missing_packets=10
repeat_manifest_digest_match=true
```

这里的 `exact` 表示压缩样本字节逐字节一致，因此无损路径的 PSNR 标记为 `+Inf`，不表示执行了重编码或图像解码计算。

## 支持边界

明确支持：

- ISO BMFF MP4/MOV 的 `ftyp/moov/trak/mdia/minf/stbl`、`stts/ctts/stsc/stsz/stco/co64/stss/elst` 索引；
- 写出 `mp4v` 视频和 `mp4a` 音频 sample entry；
- 本地 RTP 夹具中的 PT `96`（`mcrv`/映射到 `mp4v`）和 PT `97`（`mcau`/映射到 `mp4a`）；
- SSRC+序号去重、序号回绕、重排窗口、突发缺口和迟到包拒绝；
- 完整样本边界断点、`.partial` 临时文件、checkpoint 与完成后重新扫描。

未知 handler/codec/PT、损坏 box、样本越界、路径越界和索引条目超限均返回带分类及可选文件偏移的 `fault.Error`。本项目不声称支持任意外部编码或真实网络 RTP 解包；所有样例均由代码确定性生成。

## 模块

```text
cmd/mediacore              终端验收适配器
internal/clock             纳秒统一时基、时间线提交、带宽节奏模型
internal/fault             错误分类与文件偏移
internal/scan              探测、MP4/RTP 索引、摘要缓存
internal/rtp               去重、重排、缺口和迟到包保护
internal/maptrack          确定性轨道映射
internal/remux             可恢复无损 MP4 写出与重扫
internal/manifest          审计清单
internal/verify            码率、PSNR/exact、音频差异和对齐核验
internal/pipeline          RTP 恢复结果到样本索引的适配
internal/mediafix          确定性夹具生成
```
