# mediad — 可恢复的 MPEG-TS 处理内核

纯本地、纯终端的 Go 媒体处理内核。统一 90 kHz 时间基准与结构化错误模型，
通过幂等 DAG + 原子检查点实现中断安全恢复，并为每项输出提供来源追溯。

## 构建与验证

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediad demo --out /tmp/media-demo
```

故障与恢复演示：

```sh
# 截断输入：报告截断偏移，退出码 2，且不产生半成品清单
go run ./cmd/mediad demo --fault truncate --out /tmp/media-bad

# 在 fingerprint 节点后中断，再以相同参数续跑：复用已完成节点，
# 续跑清单摘要与完整运行一致，退出码 0
go run ./cmd/mediad demo --interrupt-after fingerprint --out /tmp/media-int
```

退出码约定：`0` 完成；`2` 输入损坏（同步/截断等）；`3` 依赖阻塞。

## 架构与数据流

```
TS 子系统 (ts/)                 分析子系统（共享事件时间与错误）
 188B 扫描 → PSI(CRC/版本)  ──►  slice/   切片：容差落边界、目标/实际码率
 连续计数 → PCR 时间线展开       audio/   静音：阈值 + 最短时长
        │                        fingerprint/ 内容窗口指纹（SHA-256）
        ▼
 pipeline/  DAG 调度 + 原子检查点（输入摘要+参数+版本幂等键）
        ▼
 manifest/  go-mp4 封装的 JSON 清单（ftyp + mdmd 盒），含来源偏移
```

- `internal/clock`：90 kHz 时间、33 位 PCR 与 32 位 RTP 回绕展开、`RangeError`。
- `ts`：包扫描、PAT/PMT（仅 CRC 正确的新版生效）、连续计数、PCR 时间线。
- `audio` / `slice`：静音区间与切片，边界不跨越未处理错误，时间误差 ≤1ms。
- `fingerprint`：内容寻址指纹，相同内容重跑复用，不同内容不会误合并。
- `pipeline`：拓扑序、幂等键、原子检查点、仅重跑缺失/失效节点。
- `manifest`：本地清单封装与文档哈希，证明续跑结果与完整运行一致。
- `internal/sample`：自生成正常流与回绕、计数跳变、PSI 更新、截断夹具。

## 边界与约束

- 仅处理项目定义的 MPEG-TS 包、PCR、PAT/PMT 与音频静音统计，不做通用解码。
- 单输入上限 2 GB（`ts.MaxInputSize`），流式处理，不全量载入。
- 样例数据全部由 `internal/sample` 在进程内生成，不引用外部素材。

## 关于 Go 版本

需求指定 Go 1.23，但固定依赖 `github.com/pion/rtp v1.10.6` 自身的 go.mod
声明 `go 1.24.0`，Go 工具链因此要求主模块 go 指令不低于 1.24（`go mod tidy`
会自动提升）。项目代码本身不使用 Go 1.21 之后的语言特性；该约束完全来自
被固定版本的依赖，无法在保留该版本的同时维持 `go 1.23` 指令。
