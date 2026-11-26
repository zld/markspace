# mediapack — 多平台 MPEG-TS 媒体打包/恢复内核

同一份 MPEG-TS 资源发布到多个平台/码率时，需要处理 PCR 回绕、连续计数跳变、
PAT/PMT 版本更新，并在打包中途退出后可追溯、可局部恢复。`mediapack` 是一个
纯本地、可复用的 Go 媒体处理内核：样例 TS 由代码确定性生成，不依赖外部媒体
文件或服务。

## 验收命令（在项目根目录）

```bash
go mod tidy
go build ./...
go test ./...

# 生成正常/损坏/边界样例（含 CC 跳变、PCR 回绕、PSI 切换、截断尾包、提交前退出）
go run ./cmd/mediapack demo --work ./tmp/demo --rates 2 --crash-before-commit
#   程序/二进制退出码 = 20（模拟崩溃，仅用于恢复路径）。
#   说明：`go run` 会把任意非零子退出码归一化为 1，但 stdout 会打印
#   `simulated_crash=true exit_code=20`；直接运行 go build 产物时退出码确为 20。

go run ./cmd/mediapack recover --work ./tmp/demo
#   recovered=1 duplicate_publish=0 missing_artifact=0

go run ./cmd/mediapack verify --work ./tmp/demo
#   roundtrip_rate=1.000
#   keyframe_hit_rate=1.000
#   segment_hash_ok=8/8
#   boundary_align_error_packets=0   （≤1 个 TS 包）
#   anomaly_counts ...               （异常分类计数）
#   verify=PASS                        （退出码 0）
```

破坏一个片段后局部恢复，其它片段哈希不变：

```bash
go run ./cmd/mediapack corrupt --work ./tmp/demo --id r0/seg_0001
find ./tmp/demo/segments -name '*.frag' | sort | xargs shasum -a 256 > /tmp/before
go run ./cmd/mediapack recover --work ./tmp/demo     # recovered=1 ...
find ./tmp/demo/segments -name '*.frag' | sort | xargs shasum -a 256 > /tmp/after
diff /tmp/before /tmp/after                          # 完全一致：被破坏片段被确定性重建
go run ./cmd/mediapack verify  --work ./tmp/demo     # segment_hash_ok=8/8
```

或直接运行一键脚本 `bash scripts/acceptance.sh`。

## 数据流与模块

```
sample(生成器) -> ts.Reader/PAT/PMT/CC/PCR/PES -> clock(单调媒体时钟)
                 -> index(关键帧索引) + errors(异常清单)
                 -> fanout(统一时钟切段/多码率扇出/MP4 索引/RTP 校验/扇入边界)
                 -> commit(WAL: prepared->committed) + manifest(版本化清单)
                 -> recover(校验哈希，仅重建受影响区间) -> verify(往返/命中率/哈希)
```

| 路径 | 职责 |
| --- | --- |
| `cmd/mediapack/main.go` | `demo/recover/verify/corrupt` 终端入口与退出码 |
| `internal/clock` | 33 位 PCR/PTS/DTS 回绕展开为单调 90kHz 时间 |
| `internal/errors` | 共用异常模型：区间/PID/偏移/类别/CC 缺口/恢复状态 |
| `internal/ts` | 188 字节包解析、同步字节/截断、逐 PID 连续计数、PES 时间戳、PAT/PMT/CRC/版本 |
| `internal/index` | 关键帧（IDR）到包偏移的索引与命中率 |
| `internal/sample` | 确定性生成正常+各类异常样例 |
| `internal/mp4idx` | 经 `go-mp4` 解析的 `ftyp/free` MP4 索引盒 |
| `internal/rtpcheck` | 经 `pion/rtp` 做 TS-over-RTP 结构校验 |
| `internal/manifest` | `format_version=1` 清单：产物来源偏移、长度、哈希、提交状态、异常 |
| `internal/commit` | JSONL WAL 与“写临时文件→记录路径/长度/哈希→提交状态”协议、崩溃码 20 |
| `internal/fanout` | 统一时钟区间、片段容器、多码率扇出与扇入边界/关键帧一致性 |
| `internal/recover` | 工作区编排、局部重建、往返/命中率/哈希校验 |
| `internal/safepath` | 拒绝 `..` 越界、绝对路径元素与 `s3://`/`http(s)://`/UNC 等非本地输出 |

## 关键不变量

- 流式按 188 字节处理，内存随单包/单区间受限，不缓存整份输入。
- 不补造媒体数据：同步字节错误、截断尾包只记录并定位偏移。
- CC 区分重复包与跳号，并记录前后同 PID 包之间的缺口范围。
- PAT/PMT 版本切换关联共享时钟；映射在区间结束前不完整则暂停/阻断该区间。
- PCR 回绕跨片段边界展开为 `2^33` 单调时间，PTS/DTS 与 PCR 交叉核验。
- 片段先写 `.part` 并持久化 `prepared`（路径+长度+哈希），再原子改名并提交
  `committed`；崩溃后重跑先校验/收录已有产物，不重复发布、不漏记。
- 哈希失败只重建对应 interval×rate 产物；确定性构建保证重建字节一致，其它文件不重写。
- 所有发布产物均有 SHA-256，`verify` 逐片段复核并做结构解析与扇入比对。

## 测试覆盖

`go test ./...` 覆盖：CC 跳变与关键帧索引关联、PCR 回绕跨片段边界、PSI 版本
切换中断/恢复与未恢复阻断、写入后提交前退出（WAL 重放 + 真实二进制退出码
20）、破坏片段的局部重建范围与其它片段哈希不变、路径越界/非本地拒绝、清单
格式版本、MP4/RTP 辅助解析。

依赖版本与 `go run` 退出码说明见 `VERSIONS.md`。
