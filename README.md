# mediarun

可复用的本地媒体处理内核：修复时间戳、重建可随机访问的索引，并用续跑日志与
原子提交让失败可定位、重跑可核验。纯本地运行，无外部服务依赖；样例与损坏
夹具全部由 Go 代码生成。

## 环境

- Go 1.23+（`go.mod` 固定 `go 1.23.0`，依赖版本固定并由 `go.sum` 校验）
- 可选：FFmpeg（验收命令以 Homebrew 安装；当前夹具由 Go 生成，处理流程不调用 FFmpeg）

```bash
brew install ffmpeg
go mod tidy
go build ./...
go test ./...
```

## 验收流程

```bash
go run ./cmd/mediarun fixture --dir ./tmp-fixture
go run ./cmd/mediarun process --input ./tmp-fixture --out ./tmp-out \
    --target-bitrate 1200000 --resume
go run ./cmd/mediarun verify  --input ./tmp-fixture --out ./tmp-out
```

或一键脚本（构建、测试、生成、两次 process 校验摘要/复用、verify、三个边界夹具）：

```bash
./scripts/acceptance.sh
```

`process` 输出目标/实际码率、偏差百分比、修复时间戳数、随机索引条目数、
样本数、时长、质量统计摘要、最大对齐误差、随机访问命中率、复用阶段数与输出摘要。

## 退出码

| 码 | 含义 |
|----|------|
| 0 | 处理完成且阈值校验通过 |
| 2 | 输入或边界校验失败（结构化错误含 `stage`/`track`/`path`/`offset`） |
| 3 | 续跑日志或输出提交失败 |

边界夹具位于 `<dir>/corrupt/`：

- `truncated`：盒头截断 → `stage=parse-box-header` + 首个不可读偏移，退出 2
- `zerots`：非法零 timescale → `stage=manifest` + 清单偏移，退出 2
- `dupts`：零时长重复时间戳无法推进 → `stage=repair` + moof 偏移，退出 2
- HLS `EXT-X-DISCONTINUITY` 跨 discontinuity 回退跳变：时间线保持单调并给出对齐误差

## 子系统与数据流

1. **清单与时间线**（`internal/manifest`、`internal/clock`）：解析 DASH MPD
   （校验 timescale/初始化段/序号/时长）与 HLS discontinuity；统一到 90 kHz
   整数 tick，EXTINF 用精确十进制整数运算累加，避免浮点累计误差。
2. **分片修复与索引**（`internal/parse`、`internal/repair`、`internal/index`）：
   流式逐片段解析 fMP4 与 MPEG-TS，保留原始字节偏移；盒头/样本表/负载截断分别
   报告阶段与偏移且不产出可用索引。修复回退、重复（按样本时长递增）与 32 位 RTP
   回绕；无法判定时停止该轨并保留已完成段。索引按随机访问点重建，查询返回不晚于
   目标时刻的最新可解码样本及源分片，越界返回显式边界错误。
3. **任务与核验**（`internal/journal`、`internal/pipeline`、`internal/verify`）：
   以输入摘要 + 参数摘要 + 阶段版本为幂等键；仅复用键与输出摘要均匹配的已验证阶段，
   未提交临时文件启动时清理，最终结果经 temp + fsync + rename 原子提交。溯源记录
   （`report.json`）包含源清单、分片摘要、修复映射摘要与索引摘要。

## 输出目录

```
<out>/journal.json          # 续跑日志（幂等键、状态、阶段输出摘要）
<out>/stages/*.json         # 各阶段已验证输出（可复用）
<out>/index.json            # 随机访问索引（原子提交）
<out>/report.json           # 溯源 + 指标报告（原子提交，决定输出摘要）
<out>/output.digest         # report.json 的 sha256
```

同一输入重复执行得到相同 `output_digest`，第二次运行复用全部 4 个阶段；改动任一
输入字节都会改变输入摘要，从而不会误用旧阶段结果，`verify` 也会因溯源不匹配而失败。

## 工程约定

按片段流式处理以控制内存；仅支持标准 DASH 分段 MP4（`.mp4/.m4s`）与 HLS
MPEG-TS（`.ts`），其他常见封装明确报不支持。输入目录只读，输出限定在指定目录，
拒绝路径越界与“输出位于输入内”。
