# transcode

离线分段转码与画质回归管线。面向多平台发行：按片段控制码率（平均 + 峰值约束），
逐帧对齐计算 PSNR，输出终端摘要与可解析的 JSON 报告。纯本地运行，无网络依赖。

## 运行

```sh
# 演示：生成静态 / 快速运动 / 末段短片三类夹具并端到端处理
go run ./cmd/transcode demo --out ./artifacts --segment 2s --target-kbps 900
# 或
./scripts/demo.sh

# 处理自有文件（仅本地文件，FFmpeg 可解码，输出 H.264/yuv420p/CFR）
go run ./cmd/transcode run --input a.mp4 --input b.mp4 --out ./artifacts

# 中断后恢复（校验配置摘要，不一致则拒绝混用结果）
go run ./cmd/transcode demo --out ./artifacts --resume
```

依赖：Go 1.23+，本机 PATH 中的 ffmpeg/ffprobe（libx264），gonum 0.15.1（`go mod tidy`）。

## 行为要点

- **分段**：按时长切分，末段不足整段仍计入统计；输入时间戳缺失或非单调时明确失败。
- **码率**：每段以 `-b:v`（目标平均）+ `-maxrate/-bufsize`（峰值）编码；实际码率按
  文件大小与时长汇总，容器开销单独上报，偏差以百分比表示。
- **画质**：转码结果与源片段逐帧对齐（psnr filter stats_file），报告全片/逐段均值、
  最小值与有效帧数；帧数不匹配时该段标记无效，不伪造数值。完全相同帧的 PSNR 为
  inf，按 99.0 dB 饱和计入均值（报告中注明）。
- **队列**：pending → running → success/failed/canceled，迁移经状态机校验；
  异常退出按 `--retries` 重试；全部迁移追加写入 `<out>/queue-state.jsonl`。
- **并发与中断**：`--concurrency` 同时限制任务数与 FFmpeg 进程数；SIGINT/SIGTERM
  停止接收新任务、终止子进程并持久化可恢复状态。
- **落盘**：输出目录已存在时拒绝覆盖；临时产物在 `<out>/.tmp/<job>/`，成功后
  rename 原子提交；失败清理临时文件，清理失败并入错误摘要。
- **可复现**：报告记录 FFmpeg 版本、每段完整 argv、输入摘要与统计精度（3 位小数）；
  恢复时校验配置摘要（编码参数 + FFmpeg 版本 + 输入摘要的 SHA-256）。

## 产物

```
artifacts/
  fixtures/           # demo 生成的夹具
  <job>/seg_NN.mp4    # 分段转码结果
  <job>/seg_NN.psnr   # 逐帧 PSNR
  <job>/report.json   # 单任务报告
  queue-state.jsonl   # 队列状态日志（恢复依据）
  report.json         # 汇总报告
```

## 测试

```sh
go test ./...
```
