# archivecheck

可恢复、可追溯、可核验的本地长时媒资清单处理内核。项目使用 Go 1.23、
`github.com/abema/go-mp4 v1.2.0` 和 `github.com/pion/rtp v1.8.9`，无外部服务依赖。

## 验收命令

在 macOS Terminal 中进入项目目录：

```bash
brew install go
go mod tidy
go build ./...
go test ./...

go run ./cmd/archivecheck -generate ./tmp-fixtures

# 该命令在提交第 1 个阶段迁移后模拟进程中断，按设计返回非零退出码。
go run ./cmd/archivecheck \
  -input ./tmp-fixtures/valid.json \
  -bitrates 400000,900000 \
  -resume-after 1 \
  -out ./tmp-out

# 首次恢复运行：从 tmp-out/state/<record>/journal.json 恢复，只重做未提交阶段。
go run ./cmd/archivecheck \
  -input ./tmp-fixtures/valid.json \
  -bitrates 400000,900000 \
  -out ./tmp-out

# 重复投递：规范化清单和产物摘要均保持零差异。
go run ./cmd/archivecheck \
  -input ./tmp-fixtures/valid.json \
  -bitrates 400000,900000 \
  -out ./tmp-out

go run ./cmd/archivecheck -input ./tmp-fixtures/truncated.json
go run ./cmd/archivecheck -input ./tmp-fixtures/bad-edit.json
```

也可以直接运行完整本地验收脚本：

```bash
./scripts/acceptance.sh
```

脚本会构建、测试、生成夹具、执行中断/恢复/重复投递，并断言 SHA-256、边界差、
错误类别和退出码。

## 终端输出

成功运行会打印：

- `stage_transition_count`：阶段迁移数；
- `layer_recovery ... recovery_request_tick / recovery_actual_tick`：每个码率、
  每个片段的请求边界和向前预滚到的最近可解码关键帧；
- `max_boundary_diff_tick`：所有表示层同一片段起止边界的最大共享时钟差；
- `manifest_sha256`：规范化输出清单的 SHA-256；
- `artifact_sha256` / `artifact_set_sha256`：产物集合摘要；
- `repeat_manifest_byte_diff`、`repeat_artifact_digest_diff`：重复处理差异。

共享时钟为 90 kHz，一个 tick 即允许的最大层间边界偏差。当前确定性夹具的层间
边界差为 `0`；非关键帧目标位置 36,000 会恢复到 PTS 30,000 的第 5 个同步样本。

## 退出码

编译后的 `archivecheck` 二进制遵循：

- `0`：处理和独立校验通过；
- `1`：输入、容器、编辑列表、边界、日志或处理失败；
- `2`：命令行参数错误。

`go run` 会把子进程的非零状态包装成 go 工具自身的退出码 `1`，同时打印
`exit status 2`；脚本因此额外直接构建二进制来断言参数错误退出码 2。

## 状态与产物布局

```text
tmp-fixtures/
  valid.json / valid.mp4
  truncated.json / truncated.mp4
  bad-edit.json / bad-empty.mp4 / bad-oob.mp4 / bad-reverse.mp4
  corrupt-box.* / no-keyframe.*

tmp-out/
  manifest.json
  state/<record-id>/journal.json
  state/<record-id>/output-manifest.json
  artifacts/<record-id>/<asset-id>/<bitrate>/fragment-N.rtpfrag
```

`<record-id>` 由规范化输入摘要、参数和 `model.StageVersion` 共同派生。输入摘要
变化会生成新的记录和产物目录，不复用旧产物。日志只保存元数据、阶段、游标和
摘要，不写入媒体负载。清单、日志和片段均通过同目录临时文件、`fsync` 与原子
改名落盘。

独立核对已生成清单：

```bash
go run ./cmd/archivecheck -verify ./tmp-out/manifest.json
```

## 模块

| 路径 | 职责 |
| --- | --- |
| `cmd/archivecheck` | 参数、终端输出、退出码适配 |
| `internal/model` | 规范化输入/输出清单与 SHA-256 |
| `internal/clock` | 唯一 90 kHz 共享媒体时间基准 |
| `internal/errors` | 跨模块稳定错误类别 |
| `internal/mp4` | go-mp4 box、mdhd 时基、elst、样本表扫描与关键帧恢复 |
| `internal/plan` | 阶段状态机和非法迁移错误 |
| `internal/store` | 原子 JSON 日志与每码率游标 |
| `internal/recovery` | 日志恢复点和请求/实际关键帧位置 |
| `internal/segment` | 多码率 RTP 片段扇出、pion/rtp 包边界和层间交叉校验 |
| `internal/provenance` | 来源/产物哈希与独立核验 |
| `internal/fixture` | 成功、截断、损坏 box、无关键帧和非法编辑列表夹具 |

扫描、恢复和分层处理均有独立 Go 测试；`internal/pipeline` 另有中断恢复、
幂等和新输入记录测试。
