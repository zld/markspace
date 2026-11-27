# mediacore

媒资接入处理内核：扫描 H.264 / H.265 / AV1 裸码流、恢复 RTP 丢包与乱序、对齐音画、
按客观质量门槛选型，并把验证通过的资产幂等归档到本地。

纯本地运行，无外部服务依赖。素材、包流、清单都由项目用本机 FFmpeg 7.1 现场生成。

## 环境

```sh
brew install go@1.23 ffmpeg
```

FFmpeg 必须是 7.x（验收目标 7.1）。程序优先使用
`/opt/homebrew/opt/ffmpeg@7/bin/ffmpeg`，也可用 `MEDIACORE_FFMPEG` 指定。
找不到时返回 `MC_FFMPEG_UNAVAILABLE` 并提示安装命令。

## 构建与测试

```sh
go mod tidy && go build ./... && go test ./...
```

## 运行

```sh
go run ./cmd/mediacore demo \
  --codec av1 --seed 17 --loss 3 --reorder 2 \
  --budget-ms 5000 --quality-min 30 --target-kbps 900
```

典型输出（数字随机器略有浮动，`recovered_packets` 与 `decision` 等判定字段稳定）：

```
codec=av1 seed=17
target_kbps=900 actual_kbps=893 deviation_pct=-0.71
psnr_db=41.86 quality_min_db=30.00
scan_frames=120 keyframes=8 index_quarantined=0
packets=431 lost=3 recovered_packets=3 missing_packets=0
sync_offset_ms=0.667 sync_residual_ms=10.000
manifest_entries=311 manifest_time_error_ms=0.000 migration_stage=done resumed=false noop=false
container video_sec=4.000 audio_sec=4.000 codec=av1 drift_ms=21.333
archive_digest=b5a8b869… duplicate=false
decision=accept reason="selected: quality, budget and rate satisfied, smallest output"
digest=7b10c684…
```

同样的输入和参数重复运行（不同 `--dir`）得到相同的 `digest`。摘要只覆盖判定结果，
不包含墙钟耗时、是否续跑、是否命中已有归档这些环境相关字段。

### 拒绝损坏索引

```sh
go run ./cmd/mediacore demo --corrupt-index --seed 17
```

退出码 2，stderr 带 `code=`、`codec=`、首个越界字节 `offset=`，不写归档清单。
三处故障分别对应「偏移不在帧边界」「非关键帧被标为关键帧」「偏移越界」，
全部隔离，合法条目不受影响。

### 中断与续跑

```sh
go run ./cmd/mediacore demo --interrupt-migration --seed 17             # 中断，程序退出码 3
go run ./cmd/mediacore demo --interrupt-migration --resume --seed 17   # 续跑成功
```

第一次在 `verify` 阶段后挂起，写出 `manifest/migration.point.json`。
续跑从不重复追加条目：条目数与不中断运行一致，时间误差 ≤ 1 ms，摘要相同。

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 处理与核验成功 |
| 2 | 输入损坏或质量门槛失败 |
| 3 | 可恢复中断，等待续跑 |
| 1 | 内部错误或 FFmpeg 不可用 |

`go run` 会把子进程的非零退出码统一回报为 shell 状态 1，但会在 stderr
打印 `exit status N` 行。要观察真实的 0/2/3 退出码（例如 CI 或脚本判断），
请先构建再执行，或直接使用 `scripts/accept.sh`：

```sh
go build -o mediacore ./cmd/mediacore
./mediacore demo --corrupt-index --seed 17; echo $?   # 2
```

程序自身始终按上表返回；`scripts/accept.sh` 正是用构建产物验证这些码。

## 结构

| 路径 | 职责 |
| --- | --- |
| `internal/clock` | 统一纳秒时钟、RTP 序号/时间戳扩展、单调守卫 |
| `internal/fault` | 稳定 `MC_*` 错误码、首个越界偏移、退出码映射 |
| `internal/bitstream` | Annex B / AV1 OBU 扫描、关键帧索引校验与隔离 |
| `internal/rtp` | 抖动缓冲、回绕扩展、按等待窗口判定缺口、分段恢复 |
| `internal/syncer` | 音画对齐，迟到帧不倒退，输出偏移与残余误差 |
| `internal/quality` | 质量门槛 → 处理预算 → 体积最小的固定优先级决策 |
| `internal/manifest` | v1（毫秒）/ v2（tick）清单读取并归一到纳秒 |
| `internal/migrate` | 临时清单 → 校验 → 原子替换，分阶段回滚点 |
| `internal/archive` | 源内容 SHA-256 幂等归档、go-mp4 片段时间校验 |
| `internal/media` | 本机 FFmpeg 生成、编码、PSNR、封装与探测 |
| `internal/pipeline` | 把以上阶段串成一次可复现运行 |
| `cmd/mediacore` | 终端入口 |

## 验收脚本

```sh
./scripts/accept.sh
```

脚本依次执行三条验收命令并断言：码率偏差 ≤ 10%、`psnr_db` ≥ 30、
清单时间误差 ≤ 1 ms、`recovered_packets=3`、两次运行摘要一致、
损坏索引退出 2 且不提交、中断退出 3 后续跑条目数与摘要不变。

## 实施边界

只处理本地生成的裸码流、RTP 包与版本化清单；不承诺通用容器转码或实时接收。
处理预算、等待窗口与 64 KiB 内存缓冲都由参数给定，超限明确失败而不是静默降级。
构建产物不进入仓库。
