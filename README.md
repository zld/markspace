# pwflow

本地播放故障调查工具：把 WebM/Matroska 媒体块、客户端事件和 framed RTP 记录放到统一的整数纳秒时间线上，生成可复核的片段、静音区间、SHA-256 指纹、清单和独立校验报告。

## 依赖

- macOS arm64/amd64
- `brew install go@1.23 ffmpeg`

## 一键验收

```bash
export PATH="$(brew --prefix go@1.23)/bin:$PATH"
go mod tidy
go build -o /tmp/pwflow ./cmd/pwflow
go test ./...
go run ./cmd/pwflow fixtures --dir /tmp/pw-cases --seed 23
scripts/acceptance.sh
```

`scripts/acceptance.sh` 会覆盖运行正常样本、损坏样本、独立校验、预算拒绝以及 `--stop-after 1`/检查点恢复。

## 常用命令

```bash
/tmp/pwflow run \
  --input /tmp/pw-cases/standard.webm \
  --events /tmp/pw-cases/events.jsonl \
  --rtp /tmp/pw-cases/trace.rtp \
  --out /tmp/pw-ok --budget-mib 64 --workers 4

/tmp/pwflow verify --manifest /tmp/pw-ok/manifest.json
```

损坏样本：

```bash
/tmp/pwflow run \
  --input /tmp/pw-cases/damaged.webm \
  --events /tmp/pw-cases/events.jsonl \
  --rtp /tmp/pw-cases/burst-loss.rtp \
  --out /tmp/pw-bad --budget-mib 64 --workers 4
```

恢复路径：

```bash
/tmp/pwflow run ... --out /tmp/pw-resume \
  --checkpoint /tmp/pw-state --stop-after 1   # 退出码 75
/tmp/pwflow run ... --out /tmp/pw-resume \
  --checkpoint /tmp/pw-state                  # 完成，清单与不中断运行一致
```

## 退出码

| Code | 含义 |
|---:|---|
| 0 | 完整通过 |
| 2 | 已产出且独立校验通过，但含可恢复 EBML/RTP 缺口 |
| 3 | 输入、校验或预算拒绝 |
| 75 | 已写入检查点，可续跑中断 |

## 设计约束

- 时间基准：公共时间轴为有理数 `1/1_000_000_000` 秒，区间一律 `[start_ns,end_ns)`。
- EBML：支持未知长度 Segment/Cluster；坏块记录偏移和错误码，只有确认下一个带 Timecode 的 Cluster 后才跳到确定边界，否则失败。
- RTP：按 SSRC 维护扩展序号、回绕、窗口重排和去重；窗口结束后的缺号形成显式 gap，不合成媒体样本。
- 静音：20 ms RMS 窗口，连续至少 300 ms 且不高于 `-55 dBFS`；与已映射 RTP 缺口相交的窗口不算静音。
- 片段：故障点前 2 秒、后 3 秒，严格重叠才合并；端点吸附到 500 ms 内最近关键帧，否则保留目标边界并报告关键帧距离。
- 确定性：WebM 切片使用 bitexact/去元数据参数；清单 JSON 结构固定，重复运行哈希一致。
- 边界：默认输入上限 256 MiB、最多 16 个轨道、坏块重同步扫描上限 4 MiB。
