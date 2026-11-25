# mediacore

区域直播分发场景下的可复用 Go 处理内核。它把 MP4 轨道 tick、RTP 时间戳统一到纳秒基准，提供不完整索引恢复、左闭右开切分、转场多来源映射、静音检测、内容指纹去重、不可变源摘要和参数化派生缓存。

## 运行验收

要求 Go 1.23+，无外部服务：

```bash
go mod download
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediadump --self-check
```

或：

```bash
./scripts/acceptance.sh
```

自检会在内存中生成以下样本并逐项断言：

- `normal`：三轨、剪辑点、转场重叠、静音和重复指纹。
- `truncated-box`：截断 box，只保留损坏偏移前的完整样本。
- `bad-box-length`：box 长度小于头部，报告 box 起点和轨道号。
- `missing-index`：移除 `stts`，扫描 `mcs ` 自描述样本 box 重建边界。
- `boundary-cut`：剪辑点落在截断损坏区间，错误从恢复层传到 `segment.cut`。
- `fingerprint-corrupt`：翻转负载字节，CRC 验证错误传到 `fingerprint.hash`。

退出码 `0` 表示全部断言通过。

也可以把夹具写入 `testdata/generated/`：

```bash
./scripts/generate-fixtures.sh
```

## 终端输出

每个样本输出：

- 样本名和片段数；
- 每个片段的来源轨道、统一时间区间、轨道本地时间、字节区间和样本序号；
- 静音区间和重复指纹关系；
- `time_deviation_ns`；
- 注入损坏偏移、实际失败偏移和 `byte_deviation`；
- 统一错误码、阶段、轨道、纳秒时间和字节偏移；
- `source_digest`、`derived_digest` 与稳定的 `mapping_digest`。

重复运行输出稳定；仅修改音频参数时源摘要不变、音频派生键变化，而切分派生键保持不变。

## 作为库使用

```go
import mediacore "github.com/zld/mediacore"

opts := mediacore.Options{
    Segment: mediacore.SegmentParams{
        Cuts: []mediacore.Cut{{At: 400_000_000}},
        Transitions: []mediacore.Transition{{
            ID: "xfade", Start: 400_000_000, End: 600_000_000,
            FromTrack: 1, ToTrack: 2,
        }},
    },
    Silence: mediacore.DefaultSilenceConfig(),
}

result, err := mediacore.ProcessBytes(mp4Bytes, opts)
if err != nil {
    if located, ok := mediacore.AsError(err); ok {
        // located.Code / Stage / TrackID / TimeNS / ByteOffset
        _ = located
    }
}
for _, clip := range result.Segments {
    _ = clip.Sources // 转场区间会保留多条来源轨道映射
}
```

根包 `github.com/zld/mediacore` 导出了可被外部 Go 程序直接引用的稳定别名；`internal/*` 是内核实现。也可以使用 `mediacore.ProcessFile`、`mediacore.OpenSource` 和 `mediacore.NewCache`。

即使返回错误，`result.Segments` 也可能包含损坏点前所有完整、可确认的片段。

## 包结构

- `api.go`：外部 Go 程序可直接导入的根包 API 与稳定类型别名。
- `internal/model`：纳秒基准、半开区间、轨道/样本模型和规范化校验。
- `internal/trace`：统一错误码、处理阶段、轨道、时间和字节位置。
- `internal/ingest`：`github.com/abema/go-mp4` 元数据/`stts` 解析和 `github.com/pion/rtp` 时间换算。
- `internal/recover`：无索引 box 扫描、截断边界和损坏区间恢复。
- `internal/segment`：剪辑点/转场切分和来源映射。
- `internal/audio`：振幅阈值与最短时长静音识别。
- `internal/fingerprint`：长度分帧 SHA-256 内容指纹和重复关系。
- `internal/store`：只读源摘要、模块版本+源摘要+参数摘要组成的派生键和 JSON 缓存。
- `internal/samplegen`：无外部素材依赖的 MP4 夹具生成器。
- `internal/selfcheck`：验收样本、断言和稳定报告。
- `cmd/mediadump`：终端验收入口。
- `cmd/genfixtures`：可选的本地夹具文件生成器。

## 错误类别

统一错误码包括：

- `BOX_LENGTH_OUT_OF_BOUNDS`
- `INPUT_TRUNCATED`
- `MISSING_INDEX`
- `TIMESTAMP_REGRESSION`
- `EMPTY_TRACK`
- `CUT_POINT_IN_CORRUPT_RANGE`
- `PAYLOAD_DIGEST_MISMATCH`
- `INVALID_ARGUMENT`

错误会保留最终阶段、起始阶段和阶段链；例如剪辑阶段错误仍可通过 `errors.Is` / 错误链找到底层 `INPUT_TRUNCATED`。

## 边界

本仓库只处理本地 MP4 box/时间索引与 RTP 时间信息，不执行媒体转码，也不承诺任意 MP4 容器或实时流兼容。夹具使用标准 `ftyp/moov/trak/mdia/...` 元数据 box 和可恢复的自定义 `mcs ` 样本 box，以便在无外部素材的条件下精确制造损坏和断言字节偏移。
