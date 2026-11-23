# mediad — 多轨时间线离屏导出内核

纯本地、无 GUI、无外部服务依赖的 Go 媒体处理内核。把多机位采集素材（MP4 容器、
RTP 包、字幕/章节/ID3/emsg/SEI 定时元数据）解析到统一纳秒时间基，经有界背压
流式管线导出确定性 RGBA 帧序列，并生成可逐帧复核、带哈希与质量统计的交付清单。

## 技术栈与固定版本

- Go 1.23（`go.mod` 固定 `go 1.23`）
- `github.com/abema/go-mp4 v1.7.4`（go-mp4 v1 可解析版本）
- `github.com/pion/rtp v1.8.11`
- 标准库 `testing`，无其他第三方运行时

## 目录结构

| 包 | 职责 |
|---|---|
| `timeline` | 统一纳秒时间基、轨道/事件/带上下文错误、回绕与漂移归一化、稳定排序合成 |
| `adapter` | 版本化解析器注册表（主次版本号，拒绝不兼容主版本）；mp4/rtp/metadata 适配器 |
| `stream` | 固定容量有界队列、背压、取消、文件检查点与慢消费者恢复 |
| `render` | 确定性 320×180 RGBA 离屏帧、逐帧 SHA-256、像素均值/方差、码率统计 |
| `manifest` | 帧对齐清单构建、跨阶段核验（时间误差 ≤1ns、轨道/事件/帧数守恒） |
| `engine` | 自生成夹具 + 全管线编排（解析→时间线→流管线→渲染→清单核验） |
| `cmd/mediad` | 纯终端验收入口 |

数据流：**解析适配器 → 统一时间线 → 有界流管线 → 离屏帧与质量统计 → 清单核验**，
时间线合成、帧导出与清单核验共用同一纳秒时间基与错误模型。

## 快速开始（macOS Terminal，项目目录内执行）

```bash
brew install go@1.23
go mod tidy
go build ./...
go test ./...

# 成功路径（慢消费者触发背压）
go run ./cmd/mediad demo --duration 12s --fps 25 --target-bitrate 2400000 \
  --queue-capacity 8 --consumer-delay 20ms

# 截断 + 恢复路径（坏关键帧隔离，从最后完整检查点续跑）
go run ./cmd/mediad demo --fixture truncated --recover
```

或直接 `./scripts/build.sh`、`./scripts/test.sh`、`./scripts/demo.sh`。

## 输出字段与退出码

成功路径输出：`frames=300`、`target_bps`、`actual_bps`、`bitrate_delta_pct`、
`quality_mean`、`quality_variance`、`manifest_max_error_ns=0`、`manifest_sha256`。
相同参数重跑 `manifest_sha256` 完全一致。

损坏/恢复路径额外输出：`errors`、`recovered_frames`、`first_bad_time_ns`，
恢复不重复提交已完成帧。

| 退出码 | 含义 |
|---|---|
| 0 | 完成且码率偏差在 5% 阈值内 |
| 1 | 输入损坏/解析失败或清单核验失败 |
| 2 | 参数错误 |

## 关键约束与不变量

- 帧尺寸固定 320×180，最高 30 fps；码率偏差目标 ≤5%。
- 内存上限 = 队列容量 × 最大样本大小 + 固定工作缓冲（见 `engine.FixedWorkingBuffer`）。
- 未知元数据保留原始字节与时间，绝不静默丢弃（`ParseResult.Unknown`）。
- 生产导出完成前清单保持 `incomplete`；原子写入（`.partial` → rename）。
- 进程中断后只能从完整帧检查点续跑，检查点 JSON 原子落盘。
- 五类故障均有自有夹具：截断、非法长度、未知版本/类型、乱序、时间戳回绕。
- 输入全部本地自生成，不访问任何外部媒体或在线服务。
