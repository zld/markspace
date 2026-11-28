# mediakernel

本地媒体处理内核：把带文件偏移的 RTP 分段传输事件重组为完整 MP4/MOV，
建立可随机访问的时间索引，并以可断点续跑的 DAG 调度后续任务。
纯终端形态，无外部服务依赖；样例数据全部由代码合成。

## 构建与测试

```sh
go mod tidy
go build ./...
go test ./...
```

## 验收

```sh
bash scripts/acceptance.sh
```

或逐步执行（注意 `go run` 会把子进程退出码折叠为 1，退出码语义以编译后的二进制为准）：

```sh
go run ./cmd/mediademo generate --dir .tmp/demo
go run ./cmd/mediademo verify --input .tmp/demo/good.mp4 --trace .tmp/demo/reordered.rtp --range 2/1:5/1
go run ./cmd/mediademo verify --input .tmp/demo/truncated.mp4        # 退出码 2
go run ./cmd/mediademo resume --dir .tmp/demo/interrupted --interrupt-after 3
go run ./cmd/mediademo resume --dir .tmp/demo/interrupted           # 退出码 0
```

退出码：0 成功；1 参数错误；2 处理故障（损坏输入、缺失分段、不可表示时刻、依赖阻塞）。

## 模块

| 路径 | 职责 |
| --- | --- |
| `internal/clock` | 事件时刻与轨道 tick 的精确有理数换算，不可表示即报错 |
| `internal/fault` | 统一错误模型：稳定错误码 + 字节偏移 + 轨道号 + 依赖节点 |
| `internal/rtpin` | 分段包解析；按偏移去重重排；到达间隔估计带宽并自适应等待窗 |
| `internal/spool` | 已落盘区间与校验和的持久日志（检查点） |
| `internal/mp4scan` | MP4/MOV box 校验与样本表（stts/stsc/stco/stsz）展开 |
| `internal/index` | 按轨道与时间排序的随机访问索引 |
| `internal/range` | 左闭右开区间读取，返回相交样本及来源偏移 |
| `internal/dag` | 依赖调度、幂等键、检查点续跑、幂等重试 |
| `internal/verify` | 产物 SHA-256 校验 |
| `internal/gen` | 确定性 MP4 与 RTP 轨迹合成器（测试夹具） |
| `cmd/mediademo` | generate / verify / resume 验收入口 |
