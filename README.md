# mediapipe — 可复用的本地媒体处理内核

一个纯本地、无外部服务依赖的 Go 媒体汇聚与处理内核。按显式时间线与轨道
依赖图完成帧校验、90 kHz 时基映射、色彩元数据归一、声道布局归一、确定性
重采样、响度控制，并支持阶段缓存、原子检查点与中断恢复。

## 构建与测试

```bash
go mod tidy
go build ./...
go test ./...        # 全部子系统独立测试，含 -race 可跑
```

## 演示

```bash
go run ./cmd/mediapipe demo \
  --seed 17 --frames 120 --audio-rate 44100 --channels 6 \
  --interrupt-after 2 --resume
```

输出字段：`track_count`、`frame_count`、`corrupt_first_frame`、
`corrupt_offset_bytes`、`resume_ms`、`output_sha256`、`loudness_lufs`、
`clip_count`、`reused_stages`、`blocked_stages`。

故障注入：`--corrupt truncated|hash_mismatch|timestamp_back|unknown_color`。

### 退出码

| 码 | 含义 |
|----|------|
| 0  | 成功 |
| 2  | 输入损坏但已准确定位（缺帧/截断/哈希不符/时间戳倒退/未知色彩标签） |
| 1  | 参数或依赖图无效 |

> 用编译后的二进制观察真实退出码；`go run` 会把子进程非零码统一报为 1：
> `go build -o mediapipe ./cmd/mediapipe && ./mediapipe demo ...`

## 架构（按数据流）

```
manifest 解析 → verify 校验 + clock 90kHz 时基映射
            → graph 拓扑排序
            → 视频: color 归一 → cut
              音频: layout 归一 → resample 重采样 → loudness 响度
            → store 原子检查点与缓存键 → 确定性输出摘要
```

- `internal/clock`    统一 90 kHz 整数刻度与半远离零舍入
- `internal/model`    轨道/帧元数据与可定位的分类错误
- `internal/graph`    显式依赖图、环/缺依赖/重复节点检测、确定性拓扑序
- `internal/manifest` 清单解析、非零起点/抖动/边界取整、RTP 序号与时间戳
- `internal/verify`   流式长度与 SHA-256 校验，报告首个损坏帧与字节偏移
- `internal/color`    色彩标签归一并保留来源，未知标签阻断下游
- `internal/audio`    显式下混矩阵、分数延迟 FIR 重采样、响度窗口与限峰
- `internal/store`    原子检查点（temp+fsync+rename）与阶段缓存键
- `internal/mp4box`   go-mp4 容器时基解析/写入，适配共享时钟
- `internal/fixture`  按种子自生成全部夹具，无外部素材
- `internal/engine`   图调度、缓存跳过、失败阻断与中断恢复

## 确定性与恢复保证

- 整数时基换算、缓存键、输出 SHA-256 在重复运行间逐字节一致。
- 阶段缓存键 = 输入摘要 + 参数哈希 + 显式版本号；仅全部依赖摘要匹配才跳过。
- 检查点原子落盘；中断后从最近有效阶段恢复，`resume_ms < 5000`。
- 干净运行与「中断+续跑」产出相同 `output_sha256`（有测试覆盖）。
- 校验阶段流式处理，存活堆不随总帧数增长（有内存测试覆盖）。

## 依赖说明

- `github.com/pion/rtp v1.8.11`：复用 RTP 序号（uint16）与时间戳（uint32）类型。
- `github.com/abema/go-mp4`：用于 MP4 元数据与时基结构。需求指定 v1.0.1，
  但上游该版本号不存在（v1.0.0 之后直接发布 v1.1.0），因此固定到最接近的
  有效发行版 **v1.0.0**；所用 `ReadBoxStructure`/`Marshal`/`Mvhd`/`Mdhd`
  API 在该版本可用且已通过 round-trip 测试。
