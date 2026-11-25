# assetcheck — 多声道音视频资产离线核验内核

纯本地 Go 1.23 工具链实现。统一整数采样时钟，解析 PCM/WAV 与 RIFF 块结构，
扫描采样块/关键帧间隔并定位损坏区间，按内容/封装/元数据三类指纹管理缓存，
通过 WAL 与原子提交发布可随机定位的时间索引。所有样例均由代码确定性生成，
不依赖外部媒体文件或网络服务。

## 快速开始

```bash
brew install go          # 若尚未安装 Go
go mod tidy
go build ./...
go test ./...

# 端到端验收（生成夹具、运行全部规定命令并断言退出码/摘要/恢复）
bash scripts/acceptance.sh
```

## 规定的验收命令

```bash
go run ./cmd/assetcheck -generate ./tmp-fixtures -report text
go run ./cmd/assetcheck -input ./tmp-fixtures/good.wav      -index ./tmp-index \
  -threshold sample-rate=48000,channels=6,max-gap-frames=1024,max-corrupt-ratio=0.001
go run ./cmd/assetcheck -input ./tmp-fixtures/truncated.wav -index ./tmp-index \
  -threshold max-corrupt-ratio=0.001
go run ./cmd/assetcheck -input ./tmp-fixtures/replaced.wav  -index ./tmp-index -mutate content
go run ./cmd/assetcheck -input ./tmp-fixtures/good.wav      -index ./tmp-index \
  -inject-failure after-index-prepare
go run ./cmd/assetcheck -input ./tmp-fixtures/good.wav      -index ./tmp-index -resume
```

另有两种变异轴可分别验证：

```bash
go run ./cmd/assetcheck -input ./tmp-fixtures/container.wav -index ./tmp-index -mutate container
go run ./cmd/assetcheck -input ./tmp-fixtures/metadata.wav  -index ./tmp-index -mutate metadata
```

> **关于 `go run` 的退出码**：Go 工具在子程序非零退出时，`go run` 自身统一
> 返回 `1` 并在 stderr 打印 `exit status N`；真实退出码 `N` 来自 assetcheck。
> 要在 shell 中直接核验 `0/2/3/4`，请先 `go build -o /tmp/assetcheck ./cmd/assetcheck`
> 再运行二进制。`scripts/acceptance.sh` 已据此对编译产物断言精确退出码，同时
> 原样执行所有列出的 `go run` 命令。

## 退出码契约

| 码 | 含义 |
|----|------|
| 0 | 分析完成且全部阈值通过 |
| 2 | 业务阈值超限（如损坏比例、最大间隔） |
| 3 | 输入/封装格式错误（截断头、非法 WAVE、不可读输入等） |
| 4 | 事务、WAL、索引提交或恢复失败（含 `-inject-failure`） |
| 1 | 用法错误或未分类内部错误 |

## 报告包含

内容/结构/元数据指纹、摘要 `summary-digest`、总帧数/现存帧/声道/采样率/位深/
块对齐/时长、关键帧边界数与 min/max/avg 间隔、损坏区间 `[start-frame,end-frame)`
与损坏帧数/比例、带字节偏移与帧号的错误码、逐项阈值的实测值/阈值/比较符/
带符号偏差、缓存与索引失效对象及原因、恢复动作、WAL 事务号与提交序号。

`-report json` 输出同结构 JSON。

## 三类指纹与失效矩阵

| 变化 | 变化的指纹 | 失效对象 |
|------|-----------|----------|
| 同路径内容替换（PCM 采样变化） | content | `parse-cache`（原因 `content-fingerprint-mismatch`） |
| 封装结构变化（增删 RIFF 块） | structure | 仅 `structure-cache` |
| 元数据变化（LIST/INFO 定长改写） | metadata | `time-index`（原因 `metadata-fingerprint-mismatch`） |

`-mutate content|container|metadata` 会先把当前状态写为基线缓存，再对同一路径
做确定性变异并重新核验，从而在一次运行中演示对应失效对象。

## 整数时间基准

`internal/mediaclock` 以“采样帧”为唯一累加单位；帧↔纳秒换算仅用整数乘除
（`frames * 1e9 / sampleRate`），杜绝浮点累计漂移。所有区段均为半开整数帧
区间 `[Start,End)`，支持合并、重叠判定与精确时长。

## 损坏定位与 ABLK 完整性块

生成的 WAV 在 `ABLK` 自定义 RIFF 块中保存每个采样块的起止帧与 CRC32、关键帧
标志。扫描器逐块复核 CRC：

- CRC 不符 → `E2001`，定位到该块完整帧区间；
- 文件截断/块越界 → `E1012`，非整帧尾部 → `E1014`（均带字节偏移），并映射为
  非空损坏帧区间与损坏比例；
- 重复时间边界 → `E2003`，单独报告且不污染关键帧间隔统计。

无 `ABLK` 的普通 WAV 仍可解析头/块/帧尾并把结构性截断映射为损坏区间，只是无法
凭空发现 PCM 内容比特损坏。

## 事务、WAL 与原子可见性

索引目录布局：

```
<index>/wal.log          # JSONL 追加预写日志（begin/stage/prepare/commit/applied/rollback）
<index>/manifest.json    # 内容键 -> 当前唯一可见已提交索引（临时文件+fsync+rename）
<index>/idx/<key>-<seq>.json
<index>/staging/<tx>.json # 未提交临时索引，永不在 manifest 中
<index>/cache.json
```

阶段：`scan` → `cache-update` → `index-prepare`（均可写检查点）→ `commit`。
`-inject-failure after-index-prepare` 在提交点之前注入 `E5003`（退出 4），
此时只有 staging 文件，manifest 仍指向上一个有效提交。任何启动（含 `-resume`）
都执行幂等恢复：有 commit 无 applied 则前滚发布，有 prepare 无 commit 则回滚
删除 staging。恢复后重跑得到与无中断完全相同的 `summary-digest`，提交序号可
对照 WAL 核对。所有正式文件写入均为“同目录临时文件 + fsync + 原子 rename”。

## 内存与扫描预算

- `-max-file-mb`（默认 256）：单文件读入内存的预算，超限返回 `E2010`；
- `-max-scan-frames`（默认 50,000,000）：扫描帧预算，超限附加 `E2005` 并在
  报告中给出 `last-valid-frame`（处理到的最后有效帧）。

## 目录结构

```
internal/
  errmodel/    跨模块稳定错误码区间与退出码映射
  mediaclock/  整数采样时钟与帧区间
  wav/         RIFF 块遍历、fmt/头校验、ABLK、三类指纹
  stream/      块扫描、损坏区间、间隔统计、阈值与确定性摘要
  index/       整数 PTS 随机定位键、内容寻址存储与 manifest
  cache/       三指纹缓存与失效策略
  txn/         WAL、阶段检查点、原子提交与幂等恢复
  atomicfile/  临时文件+校验和+原子重命名
  fixture/     确定性 WAV/ABLK/变异夹具生成器
  mp4check/    go-mp4 v1.3.0 封装/轨道结构核验 + 自生成 MP4
  rtpcheck/    pion/rtp v1.8.11 包结构/序号/时间戳连续性
cmd/assetcheck/ 终端适配入口与文本/JSON 报告
scripts/acceptance.sh  端到端验收
```

MP4 与 RTP 子系统各有独立测试，分别固定
`github.com/abema/go-mp4 v1.3.0` 与 `github.com/pion/rtp v1.8.11`。

## 范围边界

处理本地 PCM/WAV 多声道原始采样块与必要的 MP4 封装结构核验，不做音频修复或
编码；不覆盖任意编解码器。
