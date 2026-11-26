# replay — 赛事事件切片库与验收适配器

把带时间戳的比赛事件（击杀等）与 CMAF/fMP4 分片组映射到同一时间基准，按
“事件前 N 秒 / 事件后 M 秒”生成可独立核验的赛事片段和交付清单。纯本地运行，
不访问外部素材或服务；ffmpeg 仅用于样例渲染与验收期质量指标计算。

- Go 1.23（`go.mod` 声明 `go 1.23`）
- `github.com/abema/go-mp4 v1.3.0` 解析/写出 MP4 box
- `github.com/pion/rtp v1.8.11` 统一 RTP 时间戳换算
- ffmpeg / ffprobe（生成样例、切片、PSNR/时长探测）

## 一键验收

```bash
brew install go@1.23 ffmpeg
go mod tidy
go build ./...
go test ./...

go run ./cmd/demo --generate --out /tmp/replay-fixture
go run ./cmd/demo --input /tmp/replay-fixture --event kill-1 \
  --window-before 3s --window-after 5s \
  --target-bitrate 1800000 --workers 2 --verify
```

或直接运行固化了全部检查的脚本：

```bash
scripts/acceptance.sh            # 默认使用 /tmp/replay-fixture
scripts/acceptance.sh /tmp/my-fx
```

验收末行会打印并由 `internal/verify` 强制核对：目标/实际码率与偏差（限 10%）、
PSNR（限 30 dB）、清单与事件边界最大误差（限 100 ms）、mfhd 缺口/重复/tfdt
回退/丢失区间计数、产物与清单哈希。任一硬约束失败即 `VERIFY FAILED`。

## 退出码

| 码 | 含义 | 示例 |
|----|------|------|
| 0 | 约束通过（含诊断集被拒绝、交付集 normal 通过） | 验收命令 |
| 1 | 输入或输出约束失败 | 事件落入缺失区间、不支持的声道、预算拒绝、校验失败、输出被篡改 |
| 2 | 参数错误 | 未知 flag、缺 `--event`、窗口/码率/worker 取值非法、事件 id 不存在 |

`--input` 既可以是夹具根目录（自动发现含 `init.mp4` 的子目录，`normal` 为交付集，
其余为诊断集），也可以直接指向某个分片集目录；此时会向上一级查找 `events.json`，
该集自身即交付集，损坏/不支持会令退出码为 1。

## 夹具样例（`--generate`）

每个集合 11 个分片、约 20 s，640x360@30，默认 bt709 + stereo 48 kHz：

| 目录 | 构造 | 预期 |
|------|------|------|
| `normal` | 正常 | 出片 + 清单 + 校验通过 |
| `gap` | 第 4 片起 mfhd 序号 +1，造成序号缺口 | 记录 `MFHD_GAP`，tfdt 连续故无媒体丢失 |
| `dup` | 复制 `seg_02`（同序号） | 记录 `MFHD_DUP` 并丢弃重复片 |
| `tfdt-regress` | 第 6 片 tfdt 回退 2 s | 记录 `TFDT_REGRESSION`，不可判定区间标 `MISSING_MEDIA` |
| `truncated` | 第 5 片截断到 40% | 报告 `TRUNCATED_BOX` 与文件偏移，该片停止解析，区间标缺失 |
| `audio-conflict` | stereo 44.1 kHz | 规划归一到 48 kHz（`resample=true`） |
| `audio-unsupported` | 6 声道 | 明确失败（`ErrUnsupportedLayout`），不静默下混 |
| `color-missing` | 不写 colr | 不伪报转换（`convert=false`） |
| `color-conflict` | bt2020 标签 | 记录来源并按显式 bt709 目标转换 |

`events.json` 含区间内事件、近片尾事件与越界事件。

## 包结构（可复用库）

| 包 | 职责 |
|----|------|
| `internal/clock` | 媒体时标 ↔ `time.Duration` ↔ 32 位 RTP 时间戳；int64 溢出校验 |
| `internal/model` | 共享 `Report/Issue`、稳定问题码、哨兵错误、音视频元数据 |
| `internal/cmaf` | init/moof/mdat box 走读与长度校验、截断偏移、mfhd/tfdt/trun 提取、时间线重建 |
| `internal/events` | 事件加载/查找、前后窗口、容差接受（钳制）或拒绝 |
| `internal/audio` | 声道布局与采样率归一策略；mono/stereo 支持，其余显式失败 |
| `internal/video` | 色彩元数据策略：缺失不谎报、冲突按目标转换并记录来源 |
| `internal/planner` | 时长/目标视频码率/估算体积/处理成本与可解释拒绝原因（饱和运算防溢出） |
| `internal/worker` | 有界并发、重复 job id 去重、基于参数哈希 marker 的中断续跑 |
| `internal/manifest` | 确定性 JSON 清单与自哈希（字段顺序/排序 map 稳定） |
| `internal/verify` | 独立重读输入与 ffprobe/ffmpeg 算码率、边界、窗口、计数、PSNR 并核对哈希 |
| `cmd/demo` | 生成器、ffmpeg 渲染/切片适配器、编排与终端输出（参考用法） |

最小的时间基准用法：

```go
clk, _ := clock.New(90000, 0x5245504c) // 90 kHz 视频时标
rtpStart := clk.Advance(180000)        // 推进 2 s，返回 RTP 起点（含 32 位回绕）
d, err := clock.ToDuration(180000, 90000) // 2s, err==nil
```

完整编排（解析 → 时间线 → 窗口 → 规划 → 工作池 → 清单 → 校验）见
[`cmd/demo/run.go`](cmd/demo/run.go)。

## 确定性与修复

- 生成器与编码参数固定（`-threads 1`、bitexact、CBR），同机重复生成的分片字节一致。
- 清单自哈希覆盖窗口、时间线计数/分段、音视频策略、产物哈希与参数哈希；相同输入和
  参数（含 `--workers`）得到相同清单哈希。worker 数只影响并发度时，产物字节不变，
  但因 `workers` 被记入参数，不同 `--workers` 的清单哈希会不同。
- worker 的参数哈希包含 `init.mp4` 与窗口内实际分片的字节指纹：任何输入修复都会使
  旧 `.done.json` marker 失效并重算。删除重复片/恢复字节后重跑，清单哈希回到干净值。
- 重复 job id 只执行一次；marker 命中时不重复计入时长或事件。

## 限制

- 仅处理本地 CMAF/fMP4 与给定事件 JSON；不做网络源、DRM 或实时流。
- 单个 box ≤ 2 GiB、每 trun ≤ 2^20 sample、单文件 box 数 ≤ 2^16，超限拒绝。
- 时间线无法判定的区间显式标缺失，不做静默拼接。
- 支持 darwin/arm64 与 darwin/amd64（`GOOS=darwin GOARCH=amd64 go build ./...` 通过）。
