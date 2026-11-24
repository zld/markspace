# mediascan

可复用的**本地**远程制作素材分析内核：解析多码率 DASH 表示层与带
`#EXT-X-DISCONTINUITY` 的 HLS 播放列表，统一整数时间轴，估计时钟漂移、
展开时间戳回绕，解析 MP4 box / 样本 / 同步样本，统计关键帧间隔（GOP），
以字节偏移和媒体时间区间定位截断/损坏，并通过校验和检查点支持断点续跑。

纯终端、无 GUI、无网络服务；只处理本地 DASH/HLS 清单及其引用的 MP4 分片，
不下载、不转码、不修复媒体。所有引用路径都被限制在素材目录（默认清单所在
目录，可用 `--media-dir` 指定）之内。

- Go 1.23 语言指令（实际可用更新工具链；`pion/rtp v1.10.6` 的 go.mod 要求 `go 1.24`，故 tidy 会把指令提升到 `1.24.0`）
- `github.com/abema/go-mp4 v1.4.1`：强类型 MP4 box / 样本表 / tfdt / trun 解析
- `github.com/pion/rtp v1.10.6`：32 位 RTP 时间戳回绕互操作

## 快速开始（验收记录）

```bash
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediascan demo   --out /tmp/media-fixture
go run ./cmd/mediascan scan   --input /tmp/media-fixture/good.m3u8 \
                              --checkpoint /tmp/media-fixture/state --memory-mib 8
go run ./cmd/mediascan scan   --input /tmp/media-fixture/corrupt.m3u8 \
                              --checkpoint /tmp/media-fixture/recover --memory-mib 8
/usr/bin/time -p go run ./cmd/mediascan scan \
                              --input /tmp/media-fixture/corrupt.m3u8 \
                              --checkpoint /tmp/media-fixture/recover --memory-mib 8 --resume
go run ./cmd/mediascan verify --input /tmp/media-fixture/good.m3u8
```

或直接运行带断言的验收脚本：`scripts/acceptance.sh`（可选参数为夹具目录）。

> `go run` 会把子进程的退出码 `2` 包装成 go 工具自身的 `1` 并在 stderr 打印
> `exit status 2`。程序自身的语义退出码会打印为 `exit_code=0|1|2`；编译后的
> 二进制直接返回真实退出码。

## 退出码

| 码 | 含义 |
|----|------|
| 0 | 正常，未发现 error 级故障 |
| 2 | 定位到损坏/失配，但报告完整生成 |
| 1 | 参数错误、路径越界或不可恢复错误 |

## 终端输出字段

每次 `scan` / `verify` 都输出稳定的 `key=value` 块，必含：

```
fault_offset_bytes=<字节偏移，未知为 -1>
fault_time_ticks=<统一刻度的受影响媒体时间，未知为 -1>
gop_min_ticks=<最小关键帧间隔>
gop_max_ticks=<最大关键帧间隔>
resume_from_sample=<检查点边界对应的累计样本号>
elapsed_ms=<本次处理耗时（不含进程启动）>
memory_peak_bytes=<采样堆峰值与精确缓冲预算峰值的较大者>
sha256=<规范化结果的校验和，不含耗时/内存/恢复位置>
```

随后是确定性的故障明细（code / severity / segment / offset / time / sample / box）。

## 模块与数据流

```
manifest (HLS/DASH) ──► clock (统一 1e6 ticks/秒, 漂移, 回绕)
        │                      ▲
        ▼                      │
 stream (Budget 背压/有界读取) ──┤
        ▼                      │
 mp4 (go-mp4 + 容错 box 遍历) ──┘
        ▼
 analyze (VFR GOP 间隔 + 关键帧包围的损坏范围)
        ▼
 checkpoint (原子+校验和+.bak 安全回退) ──► pipeline.Run ──► cmd/mediascan
```

- `internal/clock`：溢出检查的整数刻度换算；有界窗口 + 钳制的最小二乘漂移
  估计（ppm）；仅当“旧值贴近模数上边界且新值贴近 0”的证据成立才展开回绕。
- `internal/model`：可定位 `Fault`（字节偏移 + 时间区间 + 样本号）与规范化报告。
- `internal/manifest`：HLS 十进制时长精确转 ticks、`#EXT-X-DISCONTINUITY`
  开启新 `Period` 但保留相邻关系；DASH 模板展开与多表示层时间对齐，差异显式
  报告而不拼接；`SafeResolve/IsWithin` 素材目录收敛。
- `internal/stream`：`Budget` 硬上限，`Take` 在达上限时阻塞；无缓冲 chunk
  通道让慢消费者直接对上游施加背压，绝不累积无界缓冲。
- `internal/mp4`：自有容错 box 头遍历精确定位非法长度/截断（偏移=实际 EOF），
  go-mp4 解码 stts/stsz/stss/stsc/stco/co64 与 moof/tfdt/trun；缺 moov/stbl
  或 fMP4 缺初始化段都报可定位故障。
- `internal/analyze`：按真实样本时长（VFR）计算相邻同步样本间隔，不假设帧率；
  损坏范围用其前/后关键帧界定。
- `internal/checkpoint`：原子 temp+rename、SHA-256 信封、保留一代 `.bak`；
  主检查点损坏时退回 `.bak`，否则退回样本 0，并写入 `checkpoint_rollback_reason`。
- `internal/pipeline`：协调各阶段、聚合规范报告、提交检查点、测量耗时与内存。

## 夹具

`demo` 由 `internal/fixture` 纯 Go 生成（无外部素材），并写出
`ground_truth.json`：正常素材、mid-sample 截断 box（含精确 cut 偏移与受害样本
时间）、32 位 tfdt 回绕 + 500ppm 漂移、discontinuity 双时间段、VFR 边界、
对齐与失配的 DASH。截断偏移输出与真值逐字节一致，时间误差为 0（小于一个样本）。

## 确定性与资源保证

- 同一输入重复运行：错误分类、区段边界、`sha256` 完全一致（测试覆盖）。
- 检查点仅在完整段/样本边界提交；中断后续跑得到与冷跑相同的校验和。
- `--memory-mib 8` 时报告的 `memory_peak_bytes` 约 1 MiB，远低于 8 MiB；
  同时用 `debug.SetMemoryLimit` 约束 Go 堆，并以 `stream.Budget` 硬限制缓冲。
