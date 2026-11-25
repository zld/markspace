# media-audit

本地、纯终端的多轨 MXF/MP4 素材审计与局部恢复演示。Go 夹具会在 `/tmp/media-audit-fixtures` 确定性生成正常轨道、元数据冲突、截断、SHA-256 校验失败、非法 KLV/box 长度以及边界/非法时间戳样本，不读取任何外部媒体文件。

## 验收命令

在项目根目录执行：

```bash
go mod tidy
go build ./...
go test ./...
go run ./cmd/media-audit --self-test --out /tmp/media-audit.json
```

也可以运行：

```bash
./scripts/acceptance.sh
./scripts/verify-repeat.sh
```

退出码：

- `0`：全部自测样本符合预期。
- `1`：处理失败或自测不变量不满足。
- `2`：命令参数错误。

`go run` 工具本身可能把子进程的退出码 `2` 显示为 `1`；直接运行 `go build -o /tmp/media-audit ./cmd/media-audit && /tmp/media-audit ...` 时退出码保持为 `2`。

## 再次执行

重复运行同一条 `--self-test --out /tmp/media-audit.json` 命令会从 `/tmp/media-audit-state` 读取已校验的 done 检查点。清单不含墙钟时间，任务键、排序和摘要均稳定；本仓库已验证两次输出文件可逐字节相同。

如需全新状态：

```bash
rm -rf /tmp/media-audit-state /tmp/media-audit-fixtures /tmp/media-audit.json
```

## 清单内容

`/tmp/media-audit.json` 包含：

- `sample_mappings`：样本到稳定任务键和 `OUT-...` 输出项的映射。
- `items[].input_path` / `input_paths` / `input_byte_range`：输入路径与字节范围。
- `items[].tracks[].unified_range`：统一到 90,000 ticks/sec 的时间区间，端点换算误差同时以 tick 和纳秒记录在 `start_conversion`、`end_conversion` 与 `deviations`。
- `items[].errors[]`：错误码、轨道、统一时间区间、字节范围和首个失败字节偏移。
- `items[].deviations[]`：时间换算偏差和生成损坏偏移与实际上报偏移的字节偏差。
- `items[].recovery`：恢复阶段、重启阶段、检查点回退、最小重算区间、依赖区间和不重复的成功区间。
- `items[].fingerprints` / `sha256`：文件内容 SHA-256、封装结构摘要和元数据版本。
- `digest_sha256`：规范化清单的稳定 SHA-256。

## 关键规则

- 元数据优先级：MXF → MP4 → 缺省值；MXF/MP4 的不同声明会记录 winner 和 loser。
- 指纹：
  - 内容变化：缓存与索引均失效。
  - 封装结构变化：结构索引失效。
  - 仅元数据版本变化：元数据缓存和相关清单失效，结构索引保留。
- 检查点：每阶段原子写入带 SHA-256 的 JSON 检查点；启动时从最后一个有效检查点的下一阶段继续，最新检查点损坏时隔离并回退到前一阶段。
- 恢复：合并重复/重叠区间；校验失败样本只重算损坏记录及其显式依赖，时间戳异常额外包含前一样本区间。
- 归并：稳定键为 `TASK-<sha256-16>`，输出 ID 为 `OUT-<sha256-12>`；清单按稳定键、样本 ID、路径排序，错误按偏移和错误码排序。互斥锁保证并发重复提交只有一个输出项。

## 模块地图

| 路径 | 职责 |
| --- | --- |
| `internal/timebase` | 有理时基、统一 tick/纳秒换算、非法时间戳拒绝 |
| `internal/errors` | 错误码、轨道、时间区间、字节偏移模型 |
| `internal/mxf` | SMPTE-UL 风格 KLV/BER 解析与确定性夹具构造 |
| `internal/mp4` | 基于 `github.com/abema/go-mp4` 的 box 遍历及偏移解析 |
| `internal/media` | 两种封装共用的轨道、样本、元数据和完整性模型 |
| `internal/metadata` | MXF/MP4/缺省元数据裁决与冲突双方 |
| `internal/source` | 内容、结构、元数据版本三段指纹 |
| `internal/cache` | 指纹相关缓存键和失效 |
| `internal/index` | 可持久化结构索引及失效 |
| `internal/task` | 状态机、阶段检查点、损坏检查点回退 |
| `internal/recover` | 区间合并/减法、最小恢复计划、单字节校验定位 |
| `internal/manifest` | 稳定键、并发归并、排序、摘要和 JSON 输出 |
| `internal/samplegen` | 全部 Go 夹具和精确损坏偏移期望 |
| `internal/pipeline` | 解析、裁决、校验、检查点、恢复和清单项组装 |
| `internal/selftest` | 生成、执行两次、核对验收不变量 |
| `cmd/media-audit` | 纯终端入口和退出码 |

## 普通输入模式

```bash
go run ./cmd/media-audit \
  --input /path/to/a.mxf,/path/to/a.mp4 \
  --out /tmp/task.json
```

逗号分隔路径会作为同一个稳定逻辑任务处理；未指定 `--task-key` 时，键由绝对路径集合派生。该模式只做识别、解析、记录和局部恢复规划，不进行媒体重编码。
