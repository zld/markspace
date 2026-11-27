# mediad — 本地音频批量处理内核

广播级内容批量交付场景下，对 Opus / AAC 的 RTP 音频包做统一规格处理与 MP4
再封装的纯本地、可复用处理内核。所有样例数据由项目自行生成，不引用外部
文件，无外部服务依赖。

## 技术栈

- Go 1.23（在 `go.mod` 固定）
- `github.com/abema/go-mp4 v1.3.0`：MP4 音轨封装与结构复核
- `github.com/pion/rtp v1.8.11`：RTP 包头、序号与载荷解析
- 标准库 `crypto/sha256`、`encoding/json`、`testing`

## 环境准备（macOS）

```sh
brew install go
```

## 构建与测试

```sh
go mod tidy
go build ./...
go test ./...
```

或一键执行：

```sh
./scripts/verify.sh
```

## 演示命令

```sh
go run ./cmd/mediad demo \
  --seed 42 \
  --budget-ms 5000 \
  --max-bytes 65536 \
  --target-rate 48000 \
  --layout stereo \
  --lufs -16 \
  --true-peak -1.0
```

演示自动生成并处理五类样例，打印单个 JSON 汇总：

1. `normal-opus`：正常流，丢包/重复为 0，峰值不高于 -1.0 dBTP，响度误差 ≤ 0.5 LU
2. `lossdup-opus`：含丢包与同内容重复包（重复不推进媒体时钟）
3. `truncated-aac`：截断包，报告阶段、包序号与偏移并隔离素材
4. `poisoned-opus`：损坏缓存，命中后重新核对摘要失败即作废并重算
5. `recovery-opus`：中断后续跑复用已完成阶段，两次运行清单 SHA-256 一致

JSON 字段涵盖：包总数、丢包数、重复数、异常区间、输入/输出采样数、输出字节
数、平均码率、LUFS、true peak、处理耗时、预算偏差、缓存命中与逐项状态。

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 全部满足 |
| 1  | 命令参数错误 |
| 2  | 超预算或质量不达标（不静默降低质量门槛） |
| 3  | 输入损坏（截断/非法包、序号冲突），素材隔离 |

演示固定包含截断样例，因此默认返回 3 是预期行为。

## 模块与数据流

```
RTP 数据报
  → internal/rtp      扩展序号整理：乱序/丢包/同内容重复/同序号冲突
  → internal/codec    Opus TOC / AAC ADTS 变长包解析，非法包稳定报错
  → internal/clock    全链路整数采样时钟
  → internal/audio    重采样（跨包滤波状态、末包补零）→ 声道归一 → 响度/峰值
  → internal/mp4      go-mp4 封装并复核时长/采样数/码率/体积
  → internal/cache    覆盖输入摘要/来源/全部处理规格的内容寻址缓存
  → internal/checkpoint  原子落盘的阶段检查点，支持中断续跑
  → internal/report   逐项结论 + 预算摘要 + 清单 SHA-256
  → cmd/mediad        终端入口
```

## 设计约束

- 全链路以整数采样刻度计时，浮点仅用于 PCM 幅度与增益计算
- 缓存命中仍重新核对来源与内容摘要，不符即作废重算
- 缓存与检查点均通过临时文件 + 原子 rename 落盘
- 路径限定在指定工作目录；缓存键为 64 位十六进制哈希，不含路径分隔符
- 内存随配置批次上限增长；不承诺实时处理或覆盖所有编码器变体
