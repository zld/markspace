# mediaarchive — 可复用 Go 媒体恢复内核

把含未知长度 EBML 元素、多轨交错块的 Matroska/WebM 文件与按 RTP 到达顺序
保存的回传包，恢复为可核验的长期归档资产：恢复后的 Matroska、归档清单和
可复查的质量摘要。纯本地运行，无外部服务或真实素材依赖，样例数据全部由
代码按种子生成。

## 构建与验收

```sh
go mod download
go build ./...
go test ./...
go run ./cmd/archivecheck --self-test --seed 41 --workers 4 --queue 32
```

成功路径打印（验收基线）：

```
target_bitrate_bps=8000000 actual_bitrate_bps=7920000 bitrate_deviation_pct=1.00
quality_recovered_packets=3 quality_unrecovered_packets=1
manifest_alignment_error_ms=0.5
truncated_cluster_offset=74
missing_packets=11
isolated_tracks=3
manifest_sha256=<hash>
manifest_sha256_rerun=<same hash>
acceptance=PASS
```

退出码：`0` 全部断言通过；`1` 验收指标越界；`2` 输入结构不可恢复。
重复执行的 `manifest_sha256` 逐字节相同。

## 模块划分

| 包 | 职责 |
|----|------|
| `internal/timeline` | 统一纳秒时间基准、固定位宽回绕时钟、统一错误类型 |
| `internal/ebml` | EBML/Matroska 解析，未知长度 Segment/Cluster、截断恢复、VINT 编解码 |
| `internal/rtp` | 抖动缓冲、乱序/重复包、窗口内恢复与窗口外缺口计数（pion/rtp） |
| `internal/track` | 声道/采样率/色彩元数据规范化，无法安全推断时隔离轨道 |
| `internal/pipeline` | 有界工作池（取消传播、队列释放、首因错误）与确定性归并 |
| `internal/archive` | 清单、恢复 Matroska 输出、go-mp4 归档索引校验、夹具与跨阶段不变量 |
| `cmd/archivecheck` | 终端验收适配器 |

数据流：EBML 解析器交付块事件 → 时间线统一为纳秒；RTP 缓冲与轨道规范化
共用同一时间基准和错误类型；有界工作池把规范化事件交给归并器；归并器按
（时间戳, 轨道号, 输入序号）稳定排序，驱动归档写入与清单生成。

## 关键不变量

- 所有内部时间为纳秒；归并后同轨时间不倒退（`pipeline.ValidateSourceOrder`）。
- 内存随队列上限和最大块大小受控：单文件读取上限 4 GiB，元素 2 GiB，
  块 64 MiB；工作池 worker ≤ 64、队列 ≤ 65536；拒绝整数溢出、越界长度。
- 归并排序稳定，重复运行产生逐字节相同的清单（`archive.HashManifest`）。
- 无法在不解码情况下计算的主观画质指标（PSNR/SSIM/VMAF 等）在清单的
  `unavailable_metrics` 中显式记录，不伪造数值。

## 损坏样例与跨阶段传播

夹具生成截断 Cluster、超长 EBML 长度、RTP 缺包与乱序、冲突采样率、缺失
色彩元数据。截断（解析阶段）、缺包（同步阶段）、布局冲突（归档阶段）分别
反映在清单的 `truncated_cluster_offsets`、`rtp` 计数和 `isolated_tracks`。

## Go 版本说明

题目目标为 Go 1.23，但指定的 `github.com/pion/rtp v1.10.6` 在其 go.mod 中
声明 `go 1.24.0`，Go 工具链不允许主模块声明低于依赖要求的版本，因此本项目
go.mod 指令为 `go 1.24.0`。代码本身不使用任何高于 1.23 的语言特性，在
Go 1.24+ 工具链上构建；本地验证使用 Go 1.27。
