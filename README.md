# mediad — 本地多机位定时元数据异常定位与片段恢复

纯终端、无外部服务的本地工具：对多机位采集得到的 MP4 / 分片 MP4 以及
WebVTT、章节、ID3、emsg、RTP 封装的 H.264/H.265 SEI 做精确时间基对齐，
按输入时间区间生成带源位置与时间信息的异常清单，并在坏片段或校验失败时
只从最近可验证（同步）边界重算受影响片段。

## 工具链

- Go（`go.mod` 固定依赖版本，见下）
- FFmpeg 9.1：仅本地生成样例与计算 PSNR，不访问任何外部素材

```sh
brew install go@1.23 ffmpeg
export PATH="$(brew --prefix go@1.23)/bin:$PATH"
go mod tidy
go build ./...
go test ./...
```

### 关于 go.mod 的 `go` 指令（客观约束）

任务指定 `pion/rtp v1.10.6`，而该版本的 `go.mod` 声明 `go 1.24.0`。Go
工具链规则是：**依赖要求的 go 版本高于主模块 `go` 指令时直接报错**，
`GOTOOLCHAIN=local` 与 `toolchain` 指令都无法绕过（已实测）。因此在保留
`go@1.23` 作为 PATH 前端的前提下，`go mod tidy` 会把 `go` 指令解析为
`1.24.0`，并由工具链自动切换到本地已缓存的 Go 1.24+ 完成构建。依赖本身
仍严格固定为 `go-mp4 v1.6.0` 与 `pion/rtp v1.10.6`。

## 验收

```sh
go run ./cmd/mediad demo --work .tmp/check --from 1s --to 5s \
  --scenarios normal,damage,replace,metadata
```

逐场景打印 `target_kbps、actual_kbps、bitrate_delta_pct、psnr_db、
manifest_max_error_ms、anomaly_count、recovered_segments、cache_hit、
index_rebuilt`（加 `--json` 输出机器可读 JSON）。

| 场景 | 验证点 |
| --- | --- |
| normal（冷/暖两次） | 码率偏差 ≤10%、PSNR ≥30dB、时间误差 ≤1ms；暖跑缓存命中、索引不重建 |
| damage | 清单报告源偏移的 `CHECKSUM_MISMATCH`，仅受影响片段重算，其余命中缓存 |
| replace | 同路径内容替换 → 新源摘要 → 索引重建、派生缓存失效 |
| metadata | 仅封装/元数据变化（追加 free box）→ 索引重建但内容未变的派生缓存复用 |
| corrupt（可选） | 源本身损坏 → 不可恢复范围写入清单，二进制退出码 2 |

退出码：可恢复/正常 `0`；参数或执行错误 `1`；不可恢复输入 `2`（清单保留）。

> 注：现代 Go 的 `go run` 会把子进程任意非零退出码归一化为 `1` 并打印
> `exit status 2`；用 `go build -o mediad ./cmd/mediad` 构建的二进制会如实
> 返回 `2`。规定的四个场景都可恢复，`go run` 下返回 `0`。

## 架构与数据流

```
采集资产 → source(CAS 不可变入库, 替换检测)
        → scan(MP4/fMP4、emsg、WebVTT、章节、ID3、RTP SEI)
        → timeline(有理数精确换算到纳秒 + 内容索引)
        → cache(按轨道内容摘要+处理参数寻址的派生缓存; 索引按源/元数据摘要+解析器版本)
        → recover(同步边界切片段; 坏片段隔离, 仅重算受影响区间)
        → manifest(时间窗清单, 稳定排序, 内容哈希命名)
        → verify(码率偏差 / 时间误差 / PSNR)
```

- `internal/model`：有理数媒体时间（精确换算、拒绝溢出）与统一异常模型。
- `internal/source`：全文件 SHA-256 内容寻址；读取中检测替换，
  `SOURCE_CHANGED` 并重试，不发布旧结果。
- `internal/scan`：go-mp4 解封装（含分片 tfdt/trun、tfhd 默认值、按 NAL
  类型识别同步帧）、自实现的规范 emsg v0/v1 解析、pion/rtp 重组 SEI。
- `internal/timeline`：精确纳秒归一化，溢出/乱序/重复事件保留原始轨道、
  偏移与时间，不静默修正；索引按源摘要、元数据摘要、解析器版本寻址。
- `internal/cache`：派生片段键只依赖轨道内容摘要 + 处理参数，因此重封装
  （仅元数据变化）可复用；索引按源/元数据摘要 + 解析器版本失效。
- `internal/recover`：以同步样本为边界切片段，缓存优先，故障隔离到单段。
- `internal/manifest`：按 from/to 过滤，字段完整、排序稳定、重复运行哈希一致。
- `internal/verify`：纯 Go 指标 + 可注入的 ffmpeg PSNR Runner。
- `internal/fixture`：ffmpeg 合成高熵源、两趟码控、四类（+corrupt）突变。
- `internal/pipeline`、`cmd/mediad`：端到端编排与终端入口。

## 工程约束

- 流式处理、有界缓冲（64KiB 读缓冲、SEI/emsg/ID3 上限、NAL 头部稀疏读取）。
- 仅本地文件，不访问外部素材，不执行输入中的路径或脚本。
- 每个子系统带独立 `testing` 测试；ffmpeg 相关用例在缺少二进制时自动跳过。
