# archivecheck

可复用的离线媒体归档校验内核：把多轨 HLS/DASH 素材恢复为统一整数媒体
时钟上的时间线，统计码率偏差与帧质量，并离屏导出可复核的确定性帧序列。

全部样例清单、fMP4 分片与 RTP 抓包都由 `internal/fixture` 在本地合成，
不引用任何外部文件或在线服务；运行过程只读输入、只写 `--out` 指定目录。

## 技术栈与依赖

- Go 1.23（标准库承担时间线、调度、统计、帧导出等全部核心逻辑）
- `github.com/abema/go-mp4 v1.3.0`：fMP4 box（styp/moof/traf/trun/mdat）解析与生成
- `github.com/pion/rtp v1.8.11`：RTP 包序号、时间戳、乱序恢复与序列化

## 模块与数据流

```
fixture(合成) ──> manifest(HLS/DASH 解析)
                 transport(去重/重排/缺口检测)
                       │
                 timeline(统一 90kHz 整数时钟, 多轨合成)
                    ┌──┴──┐
              quality    render(离屏帧 + SHA-256 像素哈希)
                 └──┬──┘
              task DAG 调度 + 幂等重试 + 增量恢复
```

| 包 | 职责 |
|---|---|
| `internal/merr` | 全系统统一错误类型，带 kind/subsystem/location |
| `internal/media` | 共享领域模型与统一时钟常量（90000 ticks/s） |
| `internal/manifest` | HLS/DASH 解析；缺时长、非法序号、重复引用、远程/穿越 URI 均可定位报错 |
| `internal/transport` | 有界重排窗口；重复包只计一次，窗口外丢失标为真实缺口 |
| `internal/timeline` | 整数/有理数时钟换算，记录换算误差，边界对齐清单，禁止浮点漂移 |
| `internal/quality` | 逐片段码率偏差（基点）、均值/最大偏差、解码/损坏帧数、亮度均值 |
| `internal/render` | 确定性帧序列（时间戳/尺寸/像素哈希），原子提交 frames.json |
| `internal/task` | 依赖 DAG；待运行/运行中/成功/失败/阻塞状态、循环拒绝、稳定键重试、失效集 |
| `internal/fixture` | 五类场景夹具生成器 |
| `internal/pipeline` | 流式接入、fMP4 样本对账、节点产物存储与端到端编排 |
| `cmd/archivecheck` | 验收入口与退出码契约 |

## 场景

| `--fixture` | 含义 |
|---|---|
| `valid` | 完整有序，正常完成 |
| `burst-loss` | 轨道 1 每分片突发丢 5 包，超出窗口 → 阻塞 |
| `repaired` | 修复后的完整素材，配合 `--resume` 做增量恢复 |
| `truncated` | mdat 物理截断，尾部样本损坏（计入 corrupt，不伪装成正常） |
| `duplicate-order` | 乱序 + 重传重复包，去重重排后哈希与 valid 一致 |

## 本地运行

```bash
brew install go
go mod tidy
go build ./...
go test ./...
./scripts/verify.sh
```

典型命令（与验收要求一致）：

```bash
# 正常样例：退出码 0，JSON 含全部量化字段，对齐误差 <= 1ms
go run ./cmd/archivecheck --fixture valid --tracks 3 \
  --bandwidth-kbps 800,420,1100 --window 4 --out /tmp/archive-frames

# 突发丢失：报告丢包数、首个阻塞依赖、受影响轨道，退出码 1
go run ./cmd/archivecheck --fixture burst-loss --tracks 3 \
  --window 2 --out /tmp/archive-bad

# 修复后增量重跑：只跑失效节点及其依赖，哈希与完整重跑一致，退出码 0
go run ./cmd/archivecheck --fixture repaired --tracks 3 --window 4 \
  --resume /tmp/archive-bad --out /tmp/archive-recovered

# 边界样例
go run ./cmd/archivecheck --fixture truncated --tracks 3 \
  --bandwidth-kbps 800,420,1100 --window 4 --out /tmp/archive-trunc
go run ./cmd/archivecheck --fixture duplicate-order --tracks 3 \
  --bandwidth-kbps 800,420,1100 --window 4 --out /tmp/archive-dup
```

## 退出码

| 码 | 含义 |
|---|---|
| 0 | 完成且无阻塞（truncated 这类已被完整计入统计的降级仍为 0） |
| 1 | 处理失败或存在阻塞依赖（如突发丢包） |
| 2 | 参数错误 |

## 工程约定

- 内存有界：重排窗口固定大小，分片逐段流式处理。
- 只读输入：不执行清单中的任何远程下载，远程/穿越 URI 直接报错。
- 输出受限：仅写 `--out`，节点产物与 frames.json 均为 temp + rename 原子提交。
- 确定性：相同输入重复运行得到相同帧数、时间戳与像素哈希。
- 容器范围：仅支持夹具覆盖的 fMP4/HLS/DASH 子集，未知 box/编码明确报
  `unsupported_media`，不做静默兼容。
