# tsrebuild

本地 MPEG-TS 节目映射重建与逐 PID 分析工具。它从同步的 188 字节传输流包重建
PAT/PMT 节目与基本流映射，记录每个映射的生效包区间，并逐 PID 统计包数与连续
计数异常，供回放索引与剪辑任务判断映射何时生效、哪些计数可信。

## 设计

传输层字段（同步字节、PID、连续计数器、传输错误标志、适配字段、负载）由
[go-astits](https://github.com/asticode/go-astits) v1.13.0 解析；PSI 段组装、
CRC 校验、版本提交与节目映射全部为自研实现。

```
TS reader → PSI assembler → PAT/PMT 解析 → 映射时间线
                                   ├→ 逐 PID 计数器（按区间归属）
                                   └→ 切片器（按区间选包）
                                            ↓
                                       JSON / 摘要报告
```

- `internal/tsreader`  流式读取 188 字节包，保留原始字节供切片。
- `internal/psi`       跨包段组装、pointer_field、CRC-32/MPEG-2、PAT/PMT 解析；
  只有 CRC 正确且同版本所需段齐全的表才会提交，残缺或损坏更新不覆盖最近有效映射。
- `internal/program`   按传输流 ID、节目号和 5 位循环版本号记录映射生效区间。
- `internal/inspect`   逐 PID 统计包数、连续计数缺口、重复计数、传输错误；缺包只
  影响该 PID 的连续性状态，不清空已提交节目表。
- `internal/segmenter` 复用同一解析与映射逻辑，按节目和包顺序选包，重映射时从新
  映射生效包起更新选择。
- `internal/fixture`   生成可逐包复算的确定性 TS 样例（不引用外部文件）。
- `internal/pipeline`  串接各阶段的共享引擎，inspect 与 segment 行为一致。
- `internal/report`    字段顺序固定的稳定 JSON 与终端摘要。

## 构建与测试

```sh
go mod download
go test ./...
go vet ./...
```

## 用法

```sh
# 生成带 PID 重映射与音频连续计数跳号的样例
go run ./cmd/tsrebuild generate --out /tmp/rebuild.ts --scenario remap-loss

# 分析并输出 JSON 报告（也可用不带 --json 的摘要；--input - 读标准输入）
go run ./cmd/tsrebuild inspect --input /tmp/rebuild.ts --program 1 --json

# 按节目切片，输出所选映射区间内的 PAT/PMT 与基本流包
go run ./cmd/tsrebuild segment --input /tmp/rebuild.ts --program 1 \
    --out /tmp/program.ts --json
```

场景：`basic`（稳定流）、`remap-loss`（视频 PID 256→300 重映射 + 音频跳号）、
`bad-crc`（损坏的 v1 更新不得生效）、`fragmented-psi`（跨多包的 PSI 段）。

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 成功 |
| 1  | 输入无法读取或解析 |
| 2  | 命令行参数错误 |

## 约束与边界

- 仅支持 188 字节 MPEG-TS，不解码音视频负载。
- 流式处理，不缓存完整 TS；每个 PID 至多保留一个在组 PSI 段，段上限 1024 字节。
- 坏 CRC 的表不会成为活动映射；包丢失后后续完整 PSI 仍可恢复。
- 5 位版本号循环；重复表、乱序段、重复包都不会重复计为新版本。
