# replayctl

赛事回放切片与可恢复交付内核。输入限定为 **MJPEG 视频 + 48 kHz 单声道 PCM** 的本地 MP4 和长度分帧 RTP 事件文件；无外部服务依赖。

## 验收命令

```sh
brew install go@1.23
go mod tidy
go build ./...
go test ./...

go run ./cmd/replayctl demo \
  --self-fixture \
  --event-ms 1000 --pre-ms 500 --post-ms 500 \
  --out .tmp/ok

go run ./cmd/replayctl verify --dir .tmp/ok
```

正常样例输出包含：

```text
frames=25
pixels=6400
audio_samples=48000
peak_dbfs=-12.04
video_align_error_ms=20
audio_boundary_error_samples=0
sha256=<64 位小写十六进制>
```

重复运行会核验已提交产物，哈希保持不变，日志仍只有一条提交记录。

一键执行构建、全部测试和下列损坏/边界/恢复验收：

```sh
./scripts/acceptance.sh
```

> Go 工具说明：`go run` 会把子进程的非零退出状态包装成 `exit status N`，且 `go run` 自身返回 1。因此要检查约定的退出码 2/75，请使用脚本中构建出的 `replayctl` 二进制，或先执行 `go build -o .tmp/replayctl ./cmd/replayctl`。

## 损坏、空段与恢复样例

```sh
go build -o .tmp/replayctl ./cmd/replayctl

# 16 字节文件，但首个 box 声明长度为 64
.tmp/replayctl demo --self-fixture --truncated-box --out .tmp/truncated
# TRUNCATED_BOX offset=16 ... ; exit=2；不创建交付目录

# 事件位于空剪辑段
.tmp/replayctl demo --self-fixture --time-gap \
  --event-ms 1000 --pre-ms 500 --post-ms 500 --out .tmp/gap
# TIME_GAP offset=<RTP 包原始偏移> raw_offset=<...> tick=<展开后的 90kHz tick> ; exit=2

# 32 位时间戳回绕夹具
.tmp/replayctl demo --self-fixture --timestamp-wrap \
  --event-ms 1000 --pre-ms 500 --post-ms 500 --out .tmp/wrap

# 首次在原子改名后、写日志前注入退出
.tmp/replayctl demo --self-fixture --recover \
  --event-ms 1000 --pre-ms 500 --post-ms 500 --out .tmp/recover
# exit=75

# 相同参数第一次重跑
... # recovered=1 published=1 duplicate=0

# 再跑一次
... # recovered=0 published=1 duplicate=0
```

恢复流程保证：

1. 同卷临时文件写入；
2. 计算并核验 SHA-256；
3. `fsync` 临时文件；
4. 原子 `rename` 到 `deliveries/<delivery-key>.mp4` 并同步目录；
5. 追加并 `fsync` `journal.jsonl`；
6. 原子写 `manifest.json`。

若在第 4 步后、第 5 步前退出，重跑会重建临时产物并与已改名文件比对哈希；一致则补记日志，不再次改名/发布。交付键为 `sha256(event_id | [start_ms,end_ms))`。

同一输出目录的交付在 `.publish.lock` 上取排他 `flock`，因此并发或交错的相同交付键运行会串行化：后到者在锁内重新读取 journal，看到已有提交后按哈希核验并计入 duplicate，不追加第二条记录。串行重复运行与单进程重跑一样，journal 保持恰好一条提交记录。

## 模块与数据流

| 路径 | 职责 |
| --- | --- |
| `internal/timebase/timebase.go` | 整数有理数、tick 换算、左闭右开区间，禁止浮点累积 |
| `internal/fault/error.go` | 全阶段统一错误：源码、阶段、首个不可读字节偏移、媒体 tick、退出码 |
| `internal/event/rtp.go` | 使用 `pion/rtp v1.10.5` 解析 RTP，按有符号半区正确展开 32 位回绕 |
| `internal/mp4/scan.go`、`tables.go` | box 越界/截断检查，使用 `go-mp4 v1.4.1` 解码 stts/stsc/stsz/stco/co64/stss |
| `internal/mp4/mux.go`、`mux_trak.go` | 确定性 MJPEG/PCM MP4 封装与同卷落盘支持 |
| `internal/index/index.go` | 随机访问点、`[start,end)` 样本索引 |
| `internal/timeline/map.go` | 多轨剪辑、空段检测、有理数转场边界、线性混合与饱和限幅 |
| `internal/render/` | 读取索引样本、JPEG 离屏合成/转场、PCM 交叉淡化 |
| `internal/validate/metrics.go` | 独立解码交付 MP4，复核帧数、像素数、采样数、峰值、对齐和哈希 |
| `internal/journal/` | 交付键、JSONL 提交日志、manifest、原子写和恢复状态 |
| `internal/pipeline/` | 编排解析 → 事件 → 映射 → 渲染 → 校验 → 提交/恢复 |
| `internal/fixture/` | 程序生成 JPEG、PCM、RTP、MP4 和四类边界夹具 |
| `cmd/replayctl/main.go`、`internal/cli/` | 纯终端入口与可测试退出码 |

## 关键不变量

- 所有媒体窗口均为 **左闭右开**。
- 索引先找起点前最近的 sync/RAP，再返回 DTS 落在 `[start,end)` 的样本。
- 转场 alpha 始终表示为 `numerator/denominator`，音频使用整数混合并饱和到 s16。
- box 声明越过物理文件时，错误偏移是第一个不可读的物理输入偏移（16 字节截断夹具为 `offset=16`）。
- 样本偏移越过文件时保留该样本偏移或 EOF 后首个不可读偏移及 DTS tick。
- 解析、映射、变换或验证失败时不追加 journal 提交记录。
- 输出路径、交付键和 manifest 产物路径均经过安全/越界检查。

## 开发说明

- `go.mod` 固定 `github.com/abema/go-mp4 v1.4.1` 与 `github.com/pion/rtp v1.10.5`。
- `pion/rtp v1.10.5` 上游 `go.mod` 声明 `go 1.24.0`；从 Go 1.23.12 启动时，Go 默认的 `GOTOOLCHAIN=auto` 会自动选择满足依赖要求的工具链。当前仓库常规命令在本机 Go 1.27 与 Homebrew `go@1.23` 的 auto 模式下均已验证。
- 测试和脚本只使用 `t.TempDir()` 或仓库 `.tmp/`；`.tmp/` 已被 `.gitignore` 忽略。
