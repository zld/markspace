# mediarc — 可续做的媒资恢复内核

纯本地、确定性的媒体归档处理内核。它把帧记录、RTP 音频包、统一时间基准、故障模型、
检查点和 MP4 输出放在同一条可恢复流水线中。样例输入由固定 seed 生成，不读取外部素材或服务。

## 环境

- macOS（Darwin）
- Go 1.23+（开发机实测可为 Go 1.27；`go.mod` 声明 `go 1.23`）
- 依赖固定在 `go.mod`：
  - `github.com/abema/go-mp4 v1.3.0`
  - `github.com/pion/rtp v1.8.11`

若尚未安装 Go：

```bash
brew install go
```

## 一键验收

```bash
./scripts/acceptance.sh
```

脚本会执行 `go mod tidy`、`go build ./...`、`go test ./...`，重建 `./tmp/accept`，
运行题目给出的 demo / resume 命令，核对关键输出、二次运行 SHA-256 稳定性，并用编译后的
二进制验证退出码 2、3、10。

也可以使用 Make：

```bash
make acceptance
```

## 题目给定命令

```bash
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediarc demo \
  --seed 17 --frames 120 --audio-packets 480 --chunk 16 \
  --inject bad-sha,truncated,late,duplicate \
  --workdir ./tmp/accept

go run ./cmd/mediarc resume --workdir ./tmp/accept --interrupt-after 3
go run ./cmd/mediarc resume --workdir ./tmp/accept
```

demo 输出包含：

```text
frame_total=120
bad_sha=1
truncated=1
late_packets=1            # 允许 >0
duplicate_packets=1       # 允许 >0
定位误差=0
chunk_count=8
```

前三次块（零基块号 0、1、2）提交后，中断摘要包含 `exit_code=10`。再次运行会输出：

```text
resumed_from_chunk=3
next_chunk_human=4
resume_elapsed_ms=<本机实测毫秒>
恢复耗时毫秒=<本机实测毫秒>
sha256=<64 位小写十六进制>
```

重复执行最终 resume 不会改变 `recovered.mp4` 的 SHA-256。

> Go 工具本身会把子进程的非零退出统一包装成 `exit status N`，`go run` 命令的 shell
> 状态可能是 1；应用摘要中的 `exit_code=10` 是稳定契约。直接运行 `go build` 产物时，
> 进程退出码分别为 0、2、3、10，验收脚本已验证这一点。

## 退出码

| 码 | 含义 |
|---:|---|
| 0 | 成功；注入的可恢复坏帧/丢包可转为显式缺失/补偿标记 |
| 2 | 原始输入损坏，或 `validate` / `demo --strict-corrupt` 发现坏 SHA、截断、长度/时间戳错误 |
| 3 | A/V 偏差超过一个视频帧周期，或未知声道布局 |
| 10 | `--interrupt-after` 到达已提交块边界，模拟中断 |
| 1 | 用法、IO 或其他未分类错误 |

示例：

```bash
go build -o ./tmp/mediarc ./cmd/mediarc

# 损坏输入 -> 2
./tmp/mediarc validate --workdir ./tmp/accept

# 音画偏移 50ms，超过 40ms 帧周期 -> 3
./tmp/mediarc demo --workdir ./tmp/sync --frames 24 --audio-packets 64 --chunk 8
./tmp/mediarc resume --workdir ./tmp/sync --av-offset-ms 50

# 未知声道布局 -> 3，不会猜测映射
./tmp/mediarc resume --workdir ./tmp/sync --source-layout mystery-9.1
```

## 处理模型

1. `internal/ingest`
   - `MRC1` 二进制帧容器：素材号、记录序号、PTS、时长、尺寸、色彩元数据、长度和 SHA-256。
   - 每条记录保留原始字节偏移；坏 SHA 定位到载荷首字节，截断定位到第一个缺失字节。
   - `MRR1` 长度前缀 RTP 容器；使用 `pion/rtp` 解析序号、时间戳、SSRC 和载荷。
2. `internal/model`
   - 90kHz 视频时间基、48kHz RTP 时钟、声明式 `ClockMap` 和一帧容差。
   - 统一 `Fault` / `Collector` / `ExitError` 错误模型。
3. `internal/buffer`
   - 扩展 uint16 RTP 序号，处理乱序、重复、窗口内迟到和窗口关闭后的缺口。
4. `internal/recovery`
   - 相同 PTS 去重：校验通过者优先，其次原始偏移较小者。
   - 按媒体时间重排；坏帧/缺帧写显式 `MISSING-FRAME:nnnnnn` 标记，不伪造图像。
   - 音频缺口写带 `Missing=true` 标记的确定性静音块，不声称恢复了原始样本。
5. `internal/normalize`
   - L16 PCM 解码/编码；确定性线性重采样，例如 44.1kHz → 48kHz。
   - 已知布局：`mono`、`stereo`、`5.1`；多声道必须给显式路由。
   - 色彩冲突保留所有 `source -> value`，冲突字段不选边、不覆盖。
6. `internal/syncer`
   - RTP 时钟率、源时间原点、媒体时间原点和起始偏移统一换算。
   - 超过一帧时返回退出码 3，并在结果中保留原始 PTS。
7. `internal/checkpoint`
   - `chunks/chunk-000003.mp4`、`.sha256` 和原子替换的 `index.json`。
   - 块先写临时文件、复算 SHA-256、fsync、原子 rename，再更新索引。
   - 启动核对输入摘要、参数摘要、索引和块哈希；索引 JSON 损坏时从块及 sidecar 重建，块损坏时回退到连续有效前缀。
8. `internal/media`
   - 使用 `go-mp4 v1.3.0` 写 `ftyp + mdat + moov`，包含 video/audio 两条 track 和 stts/stsc/stsz/stco。
   - 使用 `mp4.Probe` 回读验证轨道数、时间基和样本数。
9. `internal/pipeline` / `internal/report`
   - 分块编排、中断、最终 `recovered.mp4`、`summary.json` 和 key=value 终端摘要。

## 已声明的输入格式边界

- 视频：项目自有 `MRC1` 帧记录；合成载荷用于验证哈希、排序和分块，不承诺恢复不可解码图像。
- 音频：`MRR1` 中的 RTP 包，载荷为大端 signed-16 PCM（L16 风格）。
- 缺失图像只写显式标记；缺失音频只写可识别的静音/补偿块。
- 未知声道布局不做静默猜测。
- 所有输入、临时文件、检查点和产物均位于 `--workdir` 或仓库 `./tmp`，无网络服务。

## 测试

```bash
go test ./...
```

重点测试包括：

- 坏 SHA / 截断的素材号、序号和精确偏移；
- 重复帧“有效优先、偏移较小优先”；
- RTP 乱序、重复、迟到和窗口关闭丢包；
- 44.1kHz → 48kHz 重采样确定性；
- 5.1 → stereo 显式路由；
- 色彩冲突来源值保留；
- 索引 JSON 损坏重建和块哈希损坏回退；
- 块边界截断不改变后续块对齐；
- CLI 退出码 2、3、10；
- 中断两块/三块后续做及最终 MP4 SHA-256 重复一致。
