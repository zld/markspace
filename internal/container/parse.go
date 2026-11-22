package container

import "mq/internal/timeline"

// ParseOptions 为单素材处理限制，均可由运行参数配置。
type ParseOptions struct {
	MaxBytes  int64 // 单素材输入大小上限，0 表示不限
	MaxTracks int   // 单素材轨道数上限，0 表示不限
}

// Parse 按签名分发到 MXF/MP4 解析器，并强制执行输入限制。
// 签名未知或超出限制为致命错误；解析阶段错误为可恢复错误。
func Parse(buf []byte, opts ParseOptions) (*Asset, error) {
	if opts.MaxBytes > 0 && int64(len(buf)) > opts.MaxBytes {
		return nil, timeline.NewFatal(timeline.StageParse, 0,
			"input %d bytes exceeds limit %d", len(buf), opts.MaxBytes)
	}
	var (
		asset *Asset
		err   error
	)
	switch {
	case IsMXF(buf):
		asset, err = ParseMXF(buf)
	case IsMP4(buf):
		asset, err = ParseMP4(buf)
	default:
		return nil, timeline.NewFatal(timeline.StageParse, 0,
			"unrecognized container signature")
	}
	if err != nil {
		return nil, err
	}
	if opts.MaxTracks > 0 && len(asset.Tracks) > opts.MaxTracks {
		return nil, timeline.NewFatal(timeline.StageParse, 0,
			"track count %d exceeds limit %d", len(asset.Tracks), opts.MaxTracks)
	}
	return asset, nil
}
