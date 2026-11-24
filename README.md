# mediasync

可复用的实时媒体处理库：在移动网络丢包、乱序、时间戳回绕和采集端写入
中断下，修复 90 kHz 媒体时钟、重建可随机访问的索引，并让处理任务在
依赖失败或重试时保持确定、可追溯。纯 Go 1.23、纯本地、无外部服务。

## 模块布局

| 包 | 职责 |
|---|---|
| `internal/clock` | 统一 90 kHz 时钟、采样换算（保留余数）、时间戳回绕/倒退/漂移修复、结构化错误 |
| `internal/media` | PCM/WAV（16/24 位、1–8 声道）解析、跨块样本拼接、截断偏移与缺失字节报告 |
| `internal/rtp` | RTP 序号/时间戳解析、16 位回绕扩展、乱序缓存、突发丢包静音占位与阻塞标记 |
| `internal/index` | 随机访问索引记录、从修复区间重建、损坏/重复/越界记录清洗 |
| `internal/dag` | 任务依赖图、幂等键、有界重试、首个阻塞依赖与依赖链定位 |
| `internal/store` | 原子临时文件替换的检查点 journal、摘要门控、损坏隔离与前序重算 |
| `internal/mux` | 基于 mp4ff 的 MP4 索引封装（自定义 `mdir/midx` 盒），可回读校验 |
| `internal/fixture` | 由种子确定性生成正常/丢包/乱序/回绕/截断/损坏索引/依赖失败样例 |
| `internal/pipeline` | 串联各阶段并产出确定性摘要 |
| `cmd/mediasync` | 验收入口，打印摘要 JSON 与退出码 |

## 构建与测试

```sh
go mod tidy
go build ./...
go test ./...
```

## 验收演示

```sh
# 可恢复样例：退出码 0
go run ./cmd/mediasync --seed 7 --duration 30s --loss-burst 4 \
  --reorder 3 --truncate-audio 0 --corrupt-index 2 --retry-limit 2

# 不可恢复样例：截断字节、恢复率、阻塞依赖，退出码 2
go run ./cmd/mediasync --seed 11 --duration 5s --loss-burst 12 \
  --reorder 8 --truncate-audio 17 --corrupt-index 5 --retry-limit 1
```

> 注：`go run` 会把程序的非零退出统一包装成 shell 的 1。需要核对
> 操作系统层面的真实退出码（0/1/2）时，先 `go build -o mediasync
> ./cmd/mediasync` 再运行二进制；JSON 内的 `exit_code` 字段始终给出
> 程序自身的判定结果。

## 退出码约定

- `0`：损坏样例可恢复且无阻塞依赖
- `1`：参数错误（含长度、整数溢出、声道配置校验失败）
- `2`：输入不可恢复，或依赖达到重试上限

## 限制

- 单次输入上限 2 GiB；索引记录上限 500 万条
- 仅处理 PCM WAV 与 RTP 音频包；不实现编解码、界面、真实采集或网络传输
