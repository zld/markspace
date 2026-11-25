# medpipe

区域直播边缘节点使用的确定性媒体元数据/帧序列处理库。它把项目自定义帧记录经
解析校验、多轨纳秒时间线合成、关键帧/MP4 索引、预算决策后，导出为可复核的 PPM
帧序列与逐片段 SHA-256 哈希。

样例素材全部由代码和固定种子生成，不读取外部媒体文件，也不提供编码器或网络服务。

## 验收

```bash
go mod tidy
go build ./...
go test ./...
go run ./cmd/medpipe --selftest --seed 17 --budget-ms 5000 --max-bytes 1048576
```

成功输出形如：

```text
roundtrip=100% keyframe_hit=100% duplicates=1 corrupt=1 truncated=1 recovered=1 segment_sha256=<64-hex>
```

相同 `--seed` 和参数重复运行会得到相同的 `segment_sha256`。也可执行：

```bash
./scripts/acceptance.sh
```

退出码：

- `0`：往返一致率、关键帧命中、质量/预算和哈希核验全部通过；
- `1`：质量门槛、处理预算或体积预算无法满足，或核验/恢复失败；
- `2`：命令行参数错误。

## 断点恢复

每次阶段成功后会向 `<out-dir>/checkpoint.gob` 写入检查点。渲染等阶段失败并修正
环境后，可从最后完成的阶段继续：

```bash
go run ./cmd/medpipe --resume out/selftest/checkpoint.gob
```

恢复后的摘要必须与一次完整运行一致；该性质由 `internal/pipeline` 测试覆盖。

## 包结构

- `internal/clock`：共享有理数时间基与纳秒换算。
- `internal/fault`：带阶段、原始输入偏移和轨道号的错误/失败清单。
- `internal/ingest`：二进制帧记录、流式边界解析、SHA-256 校验和 pion/rtp 检查。
- `internal/index`：关键帧时间窗索引，以及 go-mp4 的样本描述和 `stss` 封装。
- `internal/timeline`：多轨纳秒换算、稳定排序和同轨同时间去重。
- `internal/transform`：质量门槛、处理预算、交付体积的显式优先级决策。
- `internal/render`：确定性 P6 PPM 导出和片段哈希链。
- `internal/verify`：字段往返一致率核验。
- `internal/fixture`：固定种子的正常、乱序、重复、损坏和截断素材。
- `internal/pipeline`：分阶段编排、检查点和恢复。
- `cmd/medpipe`：终端参数适配器。

## 安全与确定性约束

- 像素缓冲区只在尺寸合法且长度与尺寸一致后分配，并有单帧字节上限。
- 解析使用 `io.ReadFull` / `io.CopyN`，短输入只报告截断偏移，不会越界读取。
- PPM 头不含时间戳、主机路径或随机字段；哈希链只包含稳定的序号、轨道、时间和字节数。
- 损坏帧按长度边界隔离；校验损坏后若存在下一合法帧，会计数为一次恢复。
