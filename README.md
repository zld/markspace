# regress — 可解释的多码率直播发布回归工具

从 H.264 / H.265 / AV1 裸码流与关键帧索引出发，生成可解释的转换配置，按质量、
码率与处理预算执行，并以独立校验器确认发布结果连续且可重复。纯本地、无网络、
无图形界面，全部能力通过终端参数与标准库 `testing` 验证。

## 环境

- Go 1.23.12（通过 `GOTOOLCHAIN=go1.23.12` 固定）
- FFmpeg 7.1.x（`brew install ffmpeg@7`，默认解析
  `/opt/homebrew/opt/ffmpeg@7/bin` 或 `/usr/local/opt/ffmpeg@7/bin`，
  也可用 `FFMPEG_BIN` / `FFPROBE_BIN` 覆盖）
- 依赖在 `go.mod` 固定：`github.com/abema/go-mp4 v1.7.3`、
  `github.com/pion/rtp v1.10.6`

> `pion/rtp@v1.10.6` 自身 `go.mod` 声明 `go 1.24`，但其源码仅使用 Go 1.22
> 语言特性。为与固定的 Go 1.23.12 工具链共存，仓库在 `third_party/rtp`
> 保留 v1.10.6 完整源码并通过 `replace` 将其工具链指令降为 1.23，导入路径与
> 版本内容不变。

## 构建与测试

```sh
brew install go ffmpeg@7
GOTOOLCHAIN=go1.23.12 go mod tidy
GOTOOLCHAIN=go1.23.12 go build ./...
GOTOOLCHAIN=go1.23.12 go test ./...
GOTOOLCHAIN=go1.23.12 go vet ./...
```

## 验收场景

构建二进制以获得真实退出码（`go run` 会将子程序的非零退出统一包装为 1）：

```sh
GOTOOLCHAIN=go1.23.12 go build -o regress ./cmd/regress
```

正常样例（退出码 0）：

```sh
./regress demo --codec h264 --segments 4 --ssim-min 0.93 \
  --max-bitrate-bps 600000 --work-budget 200000
# roundtrip=100.00% keyframe_hit=100.00% segment_hash_match=4/4
# ssim_min>=0.93 bitrate_peak_bps<=600000 exit=0
```

损坏索引样例（4 条索引 1 条错位，退出码 2，不发布清单）：

```sh
./regress demo --fixture corrupt-index
# keyframe_hit=75.00% rejected=1 published=0 exit=2
```

提交前崩溃与恢复（先 75 后 0）：

```sh
./regress demo --job recovery-01 --inject crash-after-publish   # exit 75
./regress demo --job recovery-01 --resume                        # exit 0
# recovered=1 published=0 duplicate=0 segment_hash_match=4/4
```

预算不可满足（无候选通过，退出码 2，不发布）：

```sh
./regress demo --work-budget 1
```

退出码约定：`0` 成功；`1` 内部或本地依赖错误；`2` 输入被拒/预算不可满足；
`75` 产物已写入但状态未提交的临时崩溃态。

## 模块与数据流

```
fixture（FFmpeg 合成码流/索引/损坏样例）
  -> internal/scan      流式解析 AU 与随机访问点，核对关键帧索引（结构化错误，不静默修正）
  -> internal/timebase  整数有理数时基，统一纳秒/90kHz 换算与溢出检查
  -> internal/plan      固定候选梯度试编码，记录 SSIM/峰值码率/确定性工作量与淘汰理由
  -> internal/worker    限并发工作池，暂存、哈希后原子发布，失败清理半成品
  -> internal/publish   CMAF/fMP4 分片（go-mp4 复核）、清单、pion/rtp 时间审计
  -> internal/recovery  planned->processing->staged->published->committed 状态机与认领
  -> internal/verify    独立重解析分片/清单/审计，复核 SSIM、码率、边界、往返、哈希
```

三个可复用核心子系统（扫描、规划执行、发布恢复）各自带独立测试；校验器不复用
规划器的通过结论。分片边界全部来自同一张关键帧表，各表示层时长差不得超过一个
源时钟刻度。

## 边界

仅处理本地输入与 Go 代码生成的夹具；码流按字节流式读取，内存随索引与并发数受限，
不承诺实时转码；拒绝路径越界与超时子进程；运行时不访问网络；构建产物不入库。
