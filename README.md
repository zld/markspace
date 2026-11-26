# mediasync

本地多语言对白、字幕与音效时间线合成工具。它读取长度前缀容器中的 RTP/Opus
数据报、go-mp4 解析的 AAC/MP4 样本表和 JSON 字幕，生成统一整数时间线、离屏
PNG 复核帧、逐项 SHA-256 与最终交付清单。

## 快速验收

```bash
go mod tidy
go build -o /tmp/media-sync ./cmd/mediasync
go test ./...
go run ./cmd/fixturegen --dir .tmp/fixtures --seed 7

/tmp/media-sync run \
  --input .tmp/fixtures/valid \
  --out .tmp/out \
  --budget-units 100000 \
  --max-bytes 1048576

/tmp/media-sync verify --out .tmp/out

rm -rf .tmp/repeat
/tmp/media-sync run --input .tmp/fixtures/valid --out .tmp/repeat \
  --budget-units 100000 --max-bytes 1048576
/tmp/media-sync compare --a .tmp/out --b .tmp/repeat   # equal=true

/tmp/media-sync run --input .tmp/fixtures/changed --out .tmp/changed \
  --budget-units 100000 --max-bytes 1048576
/tmp/media-sync compare .tmp/out .tmp/changed          # equal=false
```

也可以直接运行完整脚本：

```bash
./scripts/acceptance.sh
```

`compare` 支持 `--a DIR --b DIR`、两个位置参数，以及 `--out/--other`、
`--expected/--actual` 等常见别名。

## 取消与恢复

```bash
rm -rf .tmp/cancel
/tmp/media-sync run --input .tmp/fixtures/valid --out .tmp/cancel \
  --cancel-after-units 4
# 退出码 130；输出 state=paused、resume_track、resume_record/next_record

/tmp/media-sync run --input .tmp/fixtures/valid --out .tmp/cancel \
  --resume --budget-units 100000 --max-bytes 1048576
/tmp/media-sync compare --a .tmp/out --b .tmp/cancel   # equal=true
```

真实 `SIGINT`/`SIGTERM` 也会沿 context 和 worker pool 传播并写入 paused
检查点。

## 异常样例与退出码

生成器额外创建：

- `.tmp/fixtures/corrupt-rtp`：非法 RTP 版本，报错含 `phase=ingest.rtp`、
  `packet_index` 和 `byte_offset`，退出码 2。
- `.tmp/fixtures/corrupt-mp4`：截断 MP4，报错含 `phase=ingest.mp4`、
  `record_index` 和 `byte_offset`，退出码 2。
- `.tmp/fixtures/overflow`：时间刻度溢出，退出码 2，且不原子提交
  `manifest.json`。
- `.tmp/fixtures/quality-drift`：字幕偏差 5 ms，退出码 3。

退出码：

| 码 | 含义 |
|---:|---|
| 0 | 成功 |
| 2 | 输入/解析/校验错误 |
| 3 | 缺包率或字幕偏差质量门槛失败 |
| 4 | 处理预算或体积限制导致 paused |
| 130 | 取消导致 paused |

## 输出

```text
out/
  manifest.json          # 最终清单、统计、预算、root_hash、逐项 SHA-256
  timeline.json          # 确定性统一时间线与质量报告
  checkpoint.json        # queued/scanning/merging/rendering/committed/paused
  frames/frame-0001.png  # 4 张离屏合成帧
  ...
```

时间刻度为 `7,056,000 ticks/second`：Opus 48 kHz 乘 147，AAC 44.1 kHz 乘
160，毫秒乘 7,056。正常固定样例输出 `tracks=3 unique=9 duplicates=1 gaps=1
loss=10.00% frames=4`，根哈希为 64 位十六进制 SHA-256。

## 模块

- `internal/media`：共享时钟换算、溢出检查和结构化错误。
- `internal/ingest`：RTP 扩展序号/重排窗口、go-mp4 AAC 样本、清单和字幕。
- `internal/timeline`：确定性归并、统计与质量门槛。
- `internal/pool`：最多 4 worker、8 项队列、context 取消传播。
- `internal/checkpoint`：原子检查点和恢复坐标。
- `internal/render`：仅用标准库 PNG 的确定性离屏帧。
- `internal/output`：目录清单、root hash、逐项校验和比较。
- `internal/pipeline`：状态与各子系统编排。
- `cmd/fixturegen`：自生成全部夹具和异常样例。

已验证 `GOOS=darwin GOARCH=arm64` 与 `GOOS=darwin GOARCH=amd64` 构建。
