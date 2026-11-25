# 弱网 RTP 轨迹回放本地媒体处理内核

一个纯本地、无外部服务依赖的 Go 媒体处理内核：把弱网 RTP 轨迹稳定切段、
标记静音、去除重复内容，并输出可核对的恢复与同步证据。

## 运行环境

- macOS（Darwin），纯本地运行，无图形界面、无网络服务。
- Go 1.23（模块 `go 1.23`，依赖在 `go.mod` 固定）。
- 依赖：`github.com/abema/go-mp4 v1.4.0`、`github.com/pion/rtp v1.8.11`。

## 构建与测试

```sh
go mod download
go build ./...
go test ./...
```

## 生成样例与恢复

样例数据全部由代码确定性生成，不引用外部文件。

```sh
make fixtures   # 生成 testdata/generated/{normal,damaged,unrecoverable}
make recover    # 从 damaged 生成 testdata/generated/repaired（不写入输入目录）
```

## 运行回归

```sh
# 正常样例
go run ./cmd/regress --input ./testdata/generated/normal --bitrates 16,32,64 --json

# 可恢复的损坏样例（附带与参考树的比对）
go run ./cmd/regress --input ./testdata/generated/damaged --recover --json
```

输出 JSON 含：`sync_error_ms`、`loss_recovery_rate`、`latency_p95_ms`、
`segments`、`silence_regions`、`duplicates`、`error_offset`。

验收阈值（正常样例）：同步误差 ≤ 20 ms，恢复率 ≥ 0.9，P95 ≤ 500 ms。
损坏样例会在 `error_code`/`error_offset` 报告偏移；修复后重跑产生与正常
输入逐字节一致的摘要。

### 退出码

| 场景 | 退出码 |
| --- | --- |
| 成功（含可恢复损坏） | 0 |
| 参数错误 | 1 |
| 输入损坏且不可恢复 | 2 |

> 注意：`go run` 不会透传所编译二进制的退出码——它把任何非零子进程状态
> 都映射为自身的 `1`（见 `go help run`）。需要核对退出码 2 时，使用
> `go build` 产物或 Makefile 目标：
>
> ```sh
> make regress                # 正常，退出 0
> make regress-damaged       # 可恢复损坏，退出 0
> make regress-unrecoverable # 不可恢复，目标内部断言二进制退出 2
> ```

## 模块与数据流

```
RTP 与容器输入 → 共享时间基准
   ingest (rtp/mp4/mxf) → meta 规范化（旋转/裁剪/色彩）
        ↓
pipeline/fanout：各表示层独立恢复（禁止跨层写补偿时间戳）
        ↓
pipeline/fanin：共享时钟汇合，按一音频采样/一视频帧容差交叉校验边界
        ↓
segment 切分 · audio 静音检测 · fingerprint 指纹去重
        ↓
report 量化摘要（确定性 JSON，禁止写入输入目录）
```

| 包 | 职责 |
| --- | --- |
| `media` | 统一时间刻度、漂移估计、可定位错误、输入字节上限 |
| `internal/rtplog` | 有界长度前缀的 RTP 记录容器 |
| `clock` | 32 位时间戳回绕展开与异常区间隔离 |
| `ingest` | RTP 解析、MP4（go-mp4）与 MXF（标准库 KLV）元数据 |
| `meta` | 旋转/裁剪/色彩的来源保留与固定优先级裁决 |
| `pipeline` | 扇出独立恢复、扇入边界交叉校验、P95 抖动 |
| `segment` | 稳定切段，输出来源包范围并显式标记损坏区间 |
| `audio` | 基于负载能量的静音区间与阈值 |
| `fingerprint` | 规范化内容指纹去重，保留首次位置与匹配位置 |
| `report` | 确定性量化摘要与输出目录保护 |
| `internal/fixture` | 正常/损坏/不可恢复样例与容器编码器 |
| `cmd/regress`、`cmd/fixture` | 终端驱动 |

## 实施边界

- 仅支持样例覆盖的 RTP 时钟率（8 kHz 音频、90 kHz 视频）与 MP4/MXF 元数据子集。
- 不承诺任意编解码器解码或实时网络接入。
- 单次输入受 `media.MaxInputBytes` 限制，越界长度一律拒绝。
- 报告禁止写入输入目录；构建产物与生成样例均不提交（见 `.gitignore`）。
