# tracecheck

离线回归工具：对保存在 Matroska/WebM（含未知长度 EBML 元素与多轨交错块）中的
游戏实时语音弱网 RTP 轨迹，生成可核验的分片清单和时间对齐的多码率表示层，
并能定位异常、限制内存、中断续跑。纯本地、不联网、不执行输入内容。

## 技术栈

- Go 1.23（go.mod 固定）
- `github.com/abema/go-mp4@v1.2.0`：EBML/Matroska 结构适配（BoxInfo 偏移模型）
- `github.com/pion/rtp@v1.8.18`：RTP 序号、时间戳、负载解析
- 标准库 `testing` / `context` / `io` / `crypto/sha256`：测试、取消、流控、摘要

## 模块与数据流

```
fixture 生成器
   │
   ▼
internal/ebml   未知/已知长度元素、VINT、边界与截断（错误带偏移+路径+错误码）
   ▼
internal/mkv    多轨交错块还原、源偏移、时间倒退/重复（只记录不修正）
   ▼
internal/rtptrace  RTP 序号/时间戳关联、丢包区间、抖动（pion/rtp）
   ▼
internal/align  统一整数纳秒刻度切段、多码率表示层对齐、超阈值分片判失败
   ▼
internal/stream 有界队列 + 背压 + 取消（峰值不超过容量）
   ▼
internal/cache  内容寻址缓存（来源标识 + 源区间 + 内容摘要三重校验）
   ▼
internal/checkpoint 仅推进到完整写入且摘要核验的分片
   ▼
internal/manifest 确定性清单（逐项结论 + manifest_sha256）
   ▼
internal/pipeline 协调以上全部子系统；cmd/tracecheck 为终端入口
```

`internal/clock` 提供统一整数纳秒时间基与全项目共用的结构化错误模型
（`clock.Code` + 偏移 + 元素路径），解析、对齐、恢复共用同一时间基与错误语义。

## 验收命令（macOS Terminal，项目目录内）

```sh
brew install go@1.23
go mod tidy
go build ./...
go test ./...

# 1) 生成样例（valid.mkv / corrupt.mkv，全部字节自包含）
go run ./cmd/tracecheck --generate --out /tmp/voice-fixtures

# 2) 正常样例：打印 segment_count/aligned_count/max_skew_ns/packet_loss/
#    queue_peak/cache_hits/manifest_sha256
go run ./cmd/tracecheck --input /tmp/voice-fixtures/valid.mkv \
  --bitrates 16000,32000 --segment-ms 1000 --queue 4 --slow-consumer-ms 20

# 3) 损坏样例 + 自测套件（截断未知长度/非法VINT/时间倒退/缓存来源不符/中断续跑）
go run ./cmd/tracecheck --input /tmp/voice-fixtures/corrupt.mkv \
  --self-test-cases --resume-dir /tmp/voice-resume
```

### 退出码约定

| 退出码 | 含义 |
| --- | --- |
| 0 | 全部规则通过 |
| 1 | 业务阈值未通过（对齐失败、skew 超限、队列峰值越界等） |
| 2 | 输入损坏或参数错误 |

## 关键保证

- **内存有界**：由队列容量与单块上限（`MaxItemBytes`）共同约束；慢消费者测试
  在 race 检测下验证峰值不超过配置容量，生产者满队列时阻塞而非扩容。
- **取消安全**：context 取消立即释放阻塞的读/写；已提交检查点保留，可从该处续跑。
- **确定性**：清单摘要剔除随缓存热度/调度变化的运行期计数（queue_peak、cache_hits），
  重复运行与中断续跑得到相同 `manifest_sha256` 与逐分片摘要。
- **缓存可信**：命中仍核对输入来源标识、源字节区间和内容摘要，任一不符即驱逐重算
  并以 `CACHE_MISMATCH` 报告原因。
- **异常可定位**：每个解析错误和时间异常都带首个可定位字节偏移、元素路径/异常区间
  与稳定错误码。

## 支持范围

仅本项目声明的 Matroska/WebM 子集（EBML 头、Segment/Info/Tracks/Cluster、
SimpleBlock/Block）与本地文件输入；不支持网络输入，不执行任何输入内容。
