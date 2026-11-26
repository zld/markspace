# medcheck

可复用的 Go 多机位媒体核验库：管理不可变源资产，扫描 H.264/H.265 Annex B 与
AV1 OBU 裸码流，建立可往返的关键帧索引，提供有界 RTP 抖动/修复缓冲、有理数
音画时间线、关键帧分段 SHA-256 与 JSON 证据。

本模块只做本地容器/码流/时间记录解析，不包含解码器或渲染器，也不访问外部服务。

## 验证

要求 Go 1.23.0；依赖固定为：

- `github.com/abema/go-mp4 v1.7.4`
- `github.com/pion/rtp v1.9.0`

```bash
go mod tidy
go build ./...
go test ./...
go run ./cmd/medcheck demo --seed 17 --window 8 \
  --out "${TMPDIR:-/tmp}/media-evidence"
```

也可运行：

```bash
./scripts/verify.sh
```

演示会代码生成三路各 12 个关键帧的码流、RTP 乱序/重复/丢包/修复/回绕样例、
截断 NAL/OBU、时间戳异常、同路径替换以及 MP4 封装/元数据变更。成功时终端包含：

```text
roundtrip=12/12
index_hit=12/12
segments=12
segment_NN sha256=<64 hex> repeat_match=true
repeat_match=12/12
recovered=1
unrecoverable_gap=1
timeline_drift_max_us=<n <= 2000>
medcheck=PASS
```

退出码：

- `0`：所有预期不变量通过；
- `1`：演示完成但核验结果/不变量不符；
- `2`：命令参数或编码输入无效。

## 包结构

| 路径 | 职责 |
| --- | --- |
| `pkg/media` | 有理数时间基准、共享错误模型、SHA-256 源摘要、不可变资产注册表、原子写入与派生物缓存、go-mp4 轨道元数据规范化 |
| `pkg/bitstream` | 流式 Annex B / AV1 OBU 边界扫描、关键帧识别、截断与非法长度错误坐标 |
| `pkg/index` | 关键帧记录、`Floor(offset)` 查询、JSON 往返、偏移映射、分段边界与损坏段标记 |
| `pkg/rtp` | pion/rtp 解析、16 位序号扩展/回绕、有界窗口重排、重复/迟到识别、带真实载荷的修复与不可恢复缺口 |
| `pkg/sync` | 32 位 RTP 时间戳扩展、音频/视频有理数换算、偏移与漂移度量 |
| `pkg/evidence` | 量化检查、分段哈希、缓存/资产/错误证据和终端摘要 |
| `cmd/medcheck` | 薄命令行适配器 |
| `internal/demo` | 确定性夹具生成与验收流程 |

## 缓存失效矩阵

比较同一逻辑路径前后的三个独立摘要：

- 码流摘要变化：码流解析、偏移映射、时间线/相关索引均失效；
- 仅封装摘要变化且码流摘要相同：码流解析缓存命中，偏移映射失效；
- 规范化轨道元数据摘要变化：码流缓存保留，时间线及相关索引失效。

资产以整文件 SHA-256 建档。同一路径内容变化会写入新的资产 ID；旧 manifest 和
对象保留，路径历史追加新的当前资产，不覆盖旧结果。

## 输出目录

```text
evidence.json             机器可读完整证据
segment-NN.bin            12 个关键帧对齐分段
sources/                  生成的三路裸码流
corrupt/                  损坏夹具
container-*.mp4           封装/元数据摘要样例
asset-registry/           不可变 blob、manifest、路径历史
derivative-cache/         内容寻址派生物
```

所有 JSON/分段均先写同目录临时文件，再原子 rename；输出路径经根目录校验，
拒绝绝对名称和 `..` 逃逸。
