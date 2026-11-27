# continuity — 直播主备源切换媒体处理内核

一个从零构建、纯本地、可续跑、可核对的 Go 媒体处理内核。它对带元数据与
SHA-256 校验和的图像帧序列完成片段切分、静音检测与内容指纹去重，并把结果
写入可审计、可逐字节复核的清单。所有阶段共享同一个纳秒时间基准。

## 技术栈与依赖

- Go 1.23（`go.mod` 固定）
- `github.com/abema/go-mp4`：MP4 清单与样本时基解析（见下方版本说明）
- `github.com/pion/rtp v1.8.11`：RTP 音频包解析与序号处理
- 标准库 `crypto/sha256`、`encoding/binary`、`context`、`io`、`testing`

> **依赖版本说明**：任务指定 go-mp4 `v1.0.4`，但上游仓库并不存在该版本
> （可用版本为 `v1.0.0`、`v1.1.0`……，`v1.0.4` 在 module proxy 上返回
> `unknown revision`）。本机 module 缓存中最近的可用版本是 `v1.0.0`，因此
> 固定为 `github.com/abema/go-mp4 v1.0.0`。pion/rtp 严格固定为 `v1.8.11`。

## 目录结构

```
internal/
  clock/        统一纳秒刻度、单位换算、溢出安全的时间运算
  fault/        含阶段/轨道/输入偏移/帧号的结构化错误
  frame/        帧记录解析、元数据与 SHA-256 校验
  source/       有界的帧序列与 RTP 包流式读取
  segment/      按间隔突变/最大时长切片（左闭右开）
  silence/      RTP 去重、有限窗口重排、静音/未知区间
  fingerprint/  负载规范化、内容指纹、正本/副本判定
  pipeline/     有界队列、背压、可取消的阶段串联
  checkpoint/   原子检查点（临时文件 + fsync + rename）
  manifest/     片段/静音/去重/摘要清单，逐条校验和
  provenance/   输入摘要、配置摘要、输出到输入的来源映射
  media/        自生成夹具、PSNR/SSIM、码率指标
  mp4adapt/     MP4 时基渲染与 go-mp4 读回对齐验收
  engine/       端到端编排、续跑、verify
cmd/
  continuity/   验收适配入口（demo/resume/verify）
  genfixtures/  夹具生成命令
scripts/        generate.sh、acceptance.sh
```

## 快速开始（macOS Terminal）

```sh
go mod tidy
go build ./...
go test ./...

# 生成夹具（可选，demo 会自行在内存中生成）
./scripts/generate.sh ./tmp-fixtures

# 中断 → 续跑 → 验收
go run ./cmd/continuity demo   --out ./tmp --interrupt-after 5
go run ./cmd/continuity resume --out ./tmp
go run ./cmd/continuity verify --out ./tmp

# 故障注入（均以非零退出码结束并打印 阶段/帧号/偏移）
go run ./cmd/continuity demo --out ./tmp-corrupt  --corrupt checksum
go run ./cmd/continuity demo --out ./tmp-boundary --case timestamp-overflow
```

也可一键运行：`./scripts/acceptance.sh`。

## 输出指标（demo/resume）

`target_bitrate`、`actual_bitrate`、`bitrate_error_pct`、`psnr_db`、`ssim`、
`manifest_alignment_ns`、`frames`、`segments`、`silence_ns`、`duplicates`。

验收阈值：帧数与生成数一致；码率偏差 ≤ 5%；PSNR ≥ 40 dB；SSIM ≥ 0.98；
清单时间对齐误差 ≤ 1 ns。PSNR/SSIM 由自生成帧与重渲染参考帧计算，码率由
有效负载字节数除以同一纳秒时基下的清单时长计算。

## 关键设计

- **统一时基**：跨包时间一律为 `int64` 纳秒；RTP/MP4 的刻度经 `clock` 包
  显式换算，所有加法/换算做溢出检查，时间倒退直接拒绝。
- **结构化错误**：每个故障携带阶段、轨道、首个可定位字节偏移与帧/包序号，
  截断、长度矛盾、校验失败均不静默跳过。
- **左闭右开切分**：边界帧只归属一个片段；内部边界取段内最后一帧时刻，
  清单起止与帧采集时刻一致。
- **静音与未知**：RTP 重复序号不重复计时；乱序在有限窗口内重排；窗口耗尽
  仍缺失的区间标记为 `unknown` 而非静音。
- **内容去重**：先规范化负载再算指纹；首个片段为正本，其余标记引用正本，
  同时保留各自时间范围与输入偏移。
- **背压与上限**：阶段间为有界 channel，队列满时上游阻塞并响应 `context`
  取消；配置最大帧数与总字节数，超限有界失败。
- **可续跑**：每阶段只在输出与摘要持久化后提交检查点；检查点绑定输入摘要
  与配置摘要，摘要变化即拒绝旧检查点并重算。派生状态始终从完整输入重建，
  因此中断后续跑与不中断运行的清单逐字节一致。
- **可核对**：每条清单记录含输入摘要、帧范围、配置版本、阶段摘要、正本
  引用与记录校验和；`verify` 重跑确定性管线并逐字节比对全部清单。

## 输出边界

仅写入 `--out` 指定目录；内存由队列容量与单帧上限约束；不访问设备或外部
网络；仓库只含源码、测试与脚本，构建产物与本地输出已在 `.gitignore` 忽略。
