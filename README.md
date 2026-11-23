# mediadist

可复核的本地媒体分发管线：RTP 输入 → 去重/重排与丢失区间记录 → H.264/H.265/AV1
码流扫描与关键帧索引校验 → 统一有理数时间线与轨道图 → 解复用、轨道映射与 MP4
无损转封装 → 哈希与统计报告。

不转码、不修改压缩媒体样本内容；全部输入由种子在本地生成，结果可逐字节复现。

## 运行

```sh
brew install go@1.23
go mod tidy && go build ./... && go test ./...
go run ./cmd/mediadist demo --seed 41 --loss-burst 3 --bandwidth-kbps 1800 --segment-ms 2000
```

退出码：`0` 处理完成且指标达标，`1` 输入或参数错误，`2` 处理完成但指标未达标。

`--out DIR` 可指定输出目录；否则写入新建临时目录，并在首行打印路径。目录内含
`media.mp4` 与 `report.json`（完整证据）。默认参数下 `media.mp4` 的 SHA-256
在重复运行之间保持一致。

`--unknown-tracks drop` 会把清单外的 AV1 轨道按参数丢弃并在 `mapping` 中记录
（`dropped=[3]`），输出 MP4 只含清单内两条轨道；`error`（默认）则直接拒绝。
被丢弃轨道不参与往返比较，因此该模式下比较槽位为 99 个。

## 输出示例（默认参数）

```
roundtrip_match=98/100
keyframe_hit=9/10
segment_sha256=<64 hex>
duplicate_packets=2
reordered_packets=4
recovered_gaps=1
damaged_spans=1
repeat_run_match=true
```

- `roundtrip_match`：100 个比较槽位中，规范化媒体内容（H.264/H.265 去掉起始码后的
  NAL 负载、AV1 完整 OBU）在转封装前后哈希一致的个数。
- `keyframe_hit`：外部关键帧索引按 `(stream, RTP 时间戳, 偏移)` 与扫描结果逐项核对的
  命中数；不匹配时用扫描结果重建索引。
- `recovered_gaps`：在提交前被重传包修复的连续缺口段数。
- `damaged_spans`：扫描发现的截断 NAL/OBU 字节区间数。
- `repeat_run_match`：同一输入与参数再跑一遍，统计、索引与逐片段哈希完全一致。
- `readback_match`：重新解析写出的 `media.mp4`，逐样本比对规范化内容摘要的命中数；
  这是对磁盘字节的校验，而非写入器自报。

## 五类故障（默认种子）

| 故障 | 注入位置 | 可观测效果 |
| --- | --- | --- |
| 重复包 | H.264/H.265 各 1 个 | `duplicate_packets=2`，去重后不进入媒体 |
| 乱序包 | 4 个包延后到达 | `reordered_packets=4`，按序号重排提交 |
| 突发丢失 | H.264 关键帧样本中间 2 包 | 样本残缺 → 索引偏移越界、片段哈希改变 |
| 重传恢复 | H.265 样本内连续 2 包 | `recovered_gaps=1`，缺口在提交前修复 |
| 截断码流 | AV1 一个 OBU 被截断 | `damaged_spans=1`，后续样本仍正常解析 |
| 错误索引 | 丢失导致的关键帧行越界 | `keyframe_hit=9/10`，索引重建 |

`--loss-burst 4`（等于分包数）会整段删除两个非关键样本：第 0 段少 2 个样本、
哈希改变，往返一致率降到 98% 以下，进程以退出码 `2` 结束——这是“处理完成但
未达标”的路径。`--loss-burst 5` 超出分包数，属参数错误，退出码 `1`。

## 模块

| 路径 | 职责 |
| --- | --- |
| `internal/merr` | 统一错误模型：阶段、流号、序号/字节偏移、可恢复标记 |
| `internal/timeline/clock.go` | 有理数时间基准、回绕展开、精确换算（无浮点漂移） |
| `internal/timeline/span.go` | 半开片段区间、边界策略、片段划分与去重 |
| `internal/transport/packet.go` | RTP 固定头/CSRC/扩展/填充解析与上限校验 |
| `internal/transport/reorder.go` | 去重、乱序缓存、提交水位、待定缺口 |
| `internal/transport/loss.go` | 已提交丢失区间、重传恢复、滑动窗口带宽估计 |
| `internal/codec/annexb.go` | H.264/H.265 Annex B 扫描与规范化内容 |
| `internal/codec/obu.go` | AV1 OBU 扫描、帧边界与截断重同步 |
| `internal/index/keyframe.go` | 关键帧索引读取、逐项校验与重建 |
| `internal/track/graph.go` | 轨道依赖图、确定性拓扑排序、环路报告 |
| `internal/track/map.go` | 输入→输出轨道映射、未知轨道策略 |
| `internal/demux/demux.go` | 按统一时间线重组样本并归属片段 |
| `internal/remux/mp4.go` | 无损 MP4 转封装、每样本证据与哈希 |
| `internal/pipeline/run.go` | 阶段编排、不变量校验、重复运行比对 |
| `internal/fixture` | 种子化 RTP/码流/索引生成与五类故障注入 |
| `cmd/mediadist` | 终端验收适配器 |

## 关键不变量

- 时间换算只用整数：`Scale` 采用 128 位中间积，长时间线不累积浮点误差。
- RTP 时间戳是任意计数器，没有墙钟含义：转封装按轨归零，容器时长只反映媒体
  跨度；原始时间戳完整保留在 `report.json` 的逐样本证据里（`pts` 与
  `container_pts`）。只有调用方显式声明 `StartOffset` 时才生成空 edit 表达延迟。
- 无法解析出编码单元的样本会被跳过并记录为 `remux/sample_skipped`，其余样本
  继续写入；`stts`/`stsz`/`stss` 始终与已写入样本数一致。
- 已提交片段不可被迟到包改写：迟到包只进入证据，不进入媒体。
- 带宽估计只影响 `buffer_cap_bytes`，从不改写时间戳。
- 转封装后每个样本的原始字节哈希与规范化单元哈希都必须与输入一致。
- 同一输入与参数重复运行，`report.json` 中除路径外的所有统计与哈希一致。
- 环形依赖被拒绝并列出环路轨道，不产生任何输出文件。
