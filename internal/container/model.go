// Package container 解析 MXF/MP4 容器元数据为统一轨道视图。
//
// 数据模型类型定义在 timeline 包（避免导入环），此处以别名再导出，
// 使 container.TrackMeta 等引用保持可用。
package container

import "mq/internal/timeline"

// 数据模型别名。
type (
	SampleMeta = timeline.SampleMeta
	CropRect   = timeline.CropRect
	ColorInfo  = timeline.ColorInfo
	TrackMeta  = timeline.TrackMeta
	Asset      = timeline.Asset
)

// 轨道类别别名。
const (
	KindVideo = timeline.KindVideo
	KindAudio = timeline.KindAudio
	KindMeta  = timeline.KindMeta
)
