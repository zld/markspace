# mediapack

远程制作管线的可复用 Go 媒体处理内核：解析多轨 CMAF/fMP4 分片组，确定性修复
mfhd 序号缺口与 tfdt 回退，重建可随机访问的关键帧索引，并以检查点支持离线批
处理中断后的安全续做。纯终端形态，无网络与外部服务依赖。

## 构建与测试

```sh
go mod tidy && go build ./... && go test ./...
```

## 验收演示

```sh
go run ./cmd/mediapack demo --work ./tmp/demo --tracks 4 --fragments 32 \
  --chunk 8 --inject gap,tfdt-backstep,truncated-box,partial-index
```

输出 `roundtrip_rate`、`keyframe_hit_rate`、`fragment_hash_match`、
`recovered_chunks`、`rejected_fragments`、`invariants` 等验收字段。
重复运行（含恢复运行与全新目录运行）产生逐片段一致的 SHA-256。

## 命令

- `mediapack demo --work DIR [--tracks N] [--fragments M] [--chunk K] [--inject a,b,c]`
  生成确定性夹具（可选注入故障），执行管线并做三轮一致性校验。
- `mediapack run --work DIR [--chunk K]` 处理 `DIR/input` 中已有的分片组，
  自动从检查点恢复。

故障注入名：`gap`、`tfdt-backstep`、`truncated-box`、`missing-tfdt`、
`dup-mfhd`、`partial-index`。

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 全部校验通过 |
| 2  | 输入或参数无效 |
| 3  | 存在不可恢复片段 |
| 4  | 索引写入或校验失败 |

## 架构

- `internal/clock` — 有理数时间基准、时基换算、可分类错误模型、轨道时间线
- `internal/model` — 轨道/声道/色彩描述与规范化告警
- `internal/cmaf` — moof/traf/mfhd/tfdt/trun 解析与边界校验、RTP 来源标记
- `internal/repair` — 逻辑序号重建、重复隔离、tfdt 修复
- `internal/index` — 关键帧索引、边界查询、分块原子持久化
- `internal/batch` — 检查点恢复、批处理编排、验收校验器
- `internal/fixture` — 确定性样例与五类故障注入
