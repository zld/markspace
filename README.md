# rateqa

离线、可续跑的分片段码率控制与画质复核工具。对 8-bit YUV420p Y4M 样片按场景
切段，用 mpeg4 编码器在量化参数 2–31 间搜索，使每段实际码率逼近目标，并逐帧
比较源与解码结果输出 PSNR。全部本地运行，无外部服务依赖。

## 环境

- macOS（Apple Silicon / Intel），Go 1.22+，FFmpeg（含 `mpeg4` 编码器）
- 依赖：`gonum.org/v1/gonum v0.15.1`

```sh
brew install go ffmpeg
go mod download
```

## 构建与测试

```sh
go test ./...
go build -o /tmp/rateqa ./cmd/rateqa
```

## 子命令

| 命令 | 作用 |
| --- | --- |
| `demo` | 程序化生成确定性 YUV420p 样片并跑完整流程 |
| `run` | 读取本地 Y4M 输入并处理 |
| `resume` | 校验指纹/参数后恢复未完成候选并跑完 |
| `report` | 从持久化任务重建汇总，不重新编码 |

常用参数：`--workspace`、`--segments`、`--frames-per-segment`、
`--target-kbps`、`--workers`（1–4）、`--fail-after`、`--input`。

## 演示（含中断与续跑）

```sh
# 八段，每段 120 帧，目标 600kbps，完成 3 个候选后模拟中断（退出码 75）
/tmp/rateqa demo --workspace /tmp/rateqa-check --segments 8 \
  --frames-per-segment 120 --target-kbps 600 --workers 3 --fail-after 3

/tmp/rateqa resume --workspace /tmp/rateqa-check
/tmp/rateqa report --workspace /tmp/rateqa-check
```

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 完成 |
| 2 | 参数错误（含输入覆盖/越界、resume 参数不匹配） |
| 3 | 媒体或编码失败（截断帧、非法尺寸、不支持色度、FFmpeg 异常） |
| 4 | 存储失败（检查点/日志读写） |
| 75 | 可续跑中断（fail-after） |

## 工作区布局

```
workspace/
  demo-source.y4m     # demo 生成的确定性源（run 时不存在）
  segments/segment_NNN.y4m
  tmp/                # 每候选临时 m4v/y4m，测量后立即删除
  events.jsonl        # 追加式事件日志（fsync）
  checkpoint.json     # 原子替换（临时文件 + rename）的状态快照
  report.jsonl        # 汇总行 + 每段一行
```

## 设计要点

- **流式处理**：Y4M 读写与 PSNR 比较均逐帧进行，内存不随片长增长。
- **拟合与回退**：Gonum QR 拟合 `log(bitrate)~QP` 与 `PSNR~QP`，最多五轮更新
  候选；实测码率关系反转或拟合失败时，切换有界中点搜索并记录原因。
- **状态机**：`queued → running → completed`，失败经 `retryable` 重试，超限
  `failed`；同一候选幂等入队、不可重复认领；崩溃后未完成项重入队。
- **持久化**：每个候选完成后追加日志并原子更新检查点；resume 校验源指纹与
  全部参数，不匹配即拒绝混用。
- **子进程安全**：FFmpeg 一律以参数数组调用，不拼接 shell；临时空间按在途
  worker 数有界。

## 模块

`cmd/rateqa` 入口；`internal/cli` 参数与退出码；`internal/media` Y4M 校验/切段、
FFmpeg 编解码、PSNR；`internal/control` QR 拟合与候选搜索；`internal/jobs` 有界
并发调度与状态机；`internal/store` 事件日志与检查点；`internal/report` 指标汇总；
`internal/app` 编排。
