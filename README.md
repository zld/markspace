# mediad — CMAF/fMP4 媒体处理内核

一个可复用的纯本地媒体处理内核，用统一时间基准和可定位错误模型完成
fMP4 分片组的**解析校验、统一时间映射、切分、静音检测、内容指纹去重**，
并通过写前日志（WAL）支持**确定性恢复**。无图形界面、无外部服务依赖。

## 技术栈与依赖

- Go 1.23（`go.mod` 声明 `go 1.23`，本地更高版本工具链可直接构建）
- `github.com/abema/go-mp4 v1.2.0`：CMAF/fMP4 box 解析
- `github.com/pion/rtp v1.8.11`：RTP 32 位时间戳回绕展开
- 仅用标准库 `testing` 做模块测试、故障注入与恢复验证

依赖版本固定在 `go.mod` / `go.sum`，`go mod tidy` 锁定依赖图。

## 架构（三个子系统）

| 子系统 | 包 | 职责 |
| --- | --- | --- |
| 解析与时间线 | `internal/mp4`、`internal/timeline`、`internal/ingest` | box 扫描与偏移定位、mfhd/tfhd/tfdt/trun 解码、序号缺口/重复校验、RTP 回绕与 tfdt 回退/漂移到共同微秒轴 |
| 媒体分析 | `internal/splitter`、`internal/silence`、`internal/fingerprint` | 最近样本边界切分（VFR 不按固定帧率推算）、PCM 幅度静音、稳定内容指纹去重 |
| 事务恢复 | `internal/txn`、`internal/pipeline` | 先写 WAL 再提交临时输出、原子 rename、按输入摘要/阶段/输出校验和恢复 |

样例由 `internal/gen` 在内存中生成（正常、边界、截断），不引用任何外部文件；
`cmd/mediad` 仅负责驱动可复用库并打印验收摘要。

## 本地运行

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediad demo --out /tmp/mediad-demo
```

`go run` 会把任何非零退出码统一报告为 1；如需观察进程真实退出码，先构建：

```sh
go build -o mediad ./cmd/mediad
./mediad demo --out /tmp/demo --scenario corrupt   # 真实退出码 2
```

### 子场景

`--scenario all|normal|boundary|corrupt`（默认 `all`）：

- `normal`：多分片、静音区间、重复内容指纹。
- `boundary`：RTP 时间戳回绕、tfdt 回退（合法回绕吸收 / 不可判定回退标错）、
  可变帧率，并在 analyze 提交后注入中断再恢复。
- `corrupt`：截断 mdat，定位实际截断偏移（±1 字节）。

## 验收 JSON 字段

`segments`、`silence_intervals`、`duplicates`、`error_file`、`error_offset`、
`error_code`、`recovery_ms`、`manifest_sha256`、`output_sha256`
（另附 `rtp_wrap_monotonic`、`timeline_monotonic`、`corrupt_exit_code` 佐证字段）。

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 成功 |
| 1 | 参数错误 |
| 2 | 输入损坏（box 截断/非法长度/缺失字段/序号冲突等） |
| 3 | 恢复摘要不匹配（输入摘要或已提交输出校验和不一致） |

错误统一为 `timeline.MediaError`，携带错误码、输入文件名与字节偏移，
不静默跳过任何畸形结构。

## 工程约定

- 输入大小、box 长度、样本数上限可通过 `mp4.Limits` 配置，超限即报错。
- 临时文件与提交输出权限仅当前用户可读写（`0600` / 目录 `0700`）。
- 同一输入重复运行产出相同清单摘要与校验和。
- 仅处理本地分片文件与内存夹具，不访问网络媒体源。
