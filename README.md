# MediaRC

MediaRC 是一个纯本地、确定性的 MP4/MOV 时间线归一化、关键帧对齐、分片清单和历史检查点恢复内核。它不做视频转码。

## 环境

- macOS / Darwin
- Go 1.23+
- 依赖锁定在 `go.mod`：
  - `github.com/abema/go-mp4 v1.3.0`：MP4/MOV box 解析与序列化
  - `github.com/pion/rtp v1.8.11`：32 位 RTP 时间戳域与回绕展开适配

安装工具链：

```bash
brew install go
```

## 验收命令

```bash
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediarc demo --seed 17 --out /tmp/mediarc-demo
go run ./cmd/mediarc verify --input /tmp/mediarc-demo

go run ./cmd/mediarc demo --corrupt truncated-box --out /tmp/mediarc-bad
go run ./cmd/mediarc demo --late-event --checkpoint /tmp/mediarc-demo/checkpoint.json
```

也可以运行完整脚本：

```bash
./scripts/acceptance.sh
```

健康样例输出包含：

```text
roundtrip_rate=1.000
keyframe_hit_rate=1.000
segment_hash_match=4/4
```

截断 box 会非零退出并打印类似：

```text
byte_offset=243
```

迟到事件恢复会打印：

```text
recomputed=true
manifest_sha256_match=true
```

## 退出码

编译后的二进制遵循以下契约；通过 `go run` 执行时，Go 工具自身可能把子进程的 `2/3` 统一显示为 `exit status N` 并返回 `1`：

- `0`：成功
- `2`：输入损坏、摘要或清单校验失败、表示层对齐超差
- `3`：命令行参数或配置错误

## 输出文件

`demo` 会生成：

- `asset.mp4`：Go 代码生成的双码率、漂移、回绕和 VFR 素材
- `manifest.json`：片段序号、统一起止纳秒时间、各表示层边界和 SHA-256
- `checkpoint.json`：输入摘要、水位、回绕状态和关键帧索引状态
- `chunks/`：逐表示层、逐片段的确定性字节块
- `manifest-recomputed.json`：迟到事件恢复后的清单

所有时间比较均使用 `model.NS` 整数纳秒。配置、容差和限制会写入清单。

## 模块结构

- `internal/model/time.go`：纳秒基准、安全转换、类型化错误和退出码
- `internal/container/`：有界 box 扫描、go-mp4 解析、样本时间戳展开和字段往返校验
- `internal/timeline/`：回绕展开、稳定锚点漂移校正、水位、缓存、检查点和确定性重算
- `internal/index/keyframe.go`：原始时间戳与统一时间并存的关键帧索引
- `internal/segment/cutter.go`：参考表示层关键帧边界和其他表示层容差对齐
- `internal/manifest/manifest.go`：确定性清单、自哈希和片段哈希校验
- `internal/hash/chunk.go`：SHA-256 工具
- `internal/fixture/generate.go`：成功、零 timescale、截断 box 和边界样例

## 安全限制

默认限制可在 `model.DefaultConfig()` 中调整：

- 单输入最大 64 MiB
- box 最大深度 32
- 单轨最多展开 4096 个样本事件
- 乱序缓存最多 4096 个事件
- 缓存窗口最大 30 秒

解析器会拒绝越界 box 长度、零 timescale 和纳秒转换溢出，并返回轨道及字节偏移。损坏路径会先删除旧 `manifest.json`，避免留下可误认为成功的结果。
