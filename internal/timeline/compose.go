package timeline

import (
	"strconv"
)

// Transform 为归一化后的几何变换：固定先旋转、后裁剪。
type Transform struct {
	Rotation int
	Crop     *CropRect
	Width    int64 // 归一化后的画幅
	Height   int64
}

// ComposedSample 为主时基下的输出样本，保留源轨道时间与偏移。
type ComposedSample struct {
	Index      int
	Time       Time // 主时基下的输出时间
	Duration   Time // 主时基下的时长
	TrackUnits int64
	Size       int64
	Offset     int64 // 源文件中的原始字节偏移
}

// ComposedTrack 为合成后的单轨。
type ComposedTrack struct {
	Meta      TrackMeta
	Transform Transform
	Color     ColorInfo
	Samples   []ComposedSample
}

// Timeline 为主时基下合成的多轨时间线。
type Timeline struct {
	Scale     int64
	Tracks    []ComposedTrack
	Decisions []Decision
}

// ComposeOptions 为合成参数。
type ComposeOptions struct {
	ColorPriority []string // 色彩来源优先级，空为默认
}

// Compose 将素材的视频、音频与辅助轨合成到统一主时基。
// 旋转与裁剪按固定顺序归一化；色彩冲突按优先级裁决并记录；
// 输出时间禁止倒退。
func Compose(asset *Asset, opts ComposeOptions) (*Timeline, error) {
	tl := &Timeline{Scale: MasterScale}
	for i := range asset.Tracks {
		src := &asset.Tracks[i]
		ct, decs, err := composeTrack(src, opts)
		if err != nil {
			return nil, err
		}
		tl.Tracks = append(tl.Tracks, *ct)
		tl.Decisions = append(tl.Decisions, decs...)
	}
	return tl, nil
}

func composeTrack(src *TrackMeta, opts ComposeOptions) (*ComposedTrack, []Decision, error) {
	ct := &ComposedTrack{Meta: *src}
	if src.EditRate.Num <= 0 || src.EditRate.Den <= 0 {
		return nil, nil, NewErr(StageTimeline, src.Name, src.OffsetOf("edit_rate"),
			"invalid edit rate %d/%d", src.EditRate.Num, src.EditRate.Den)
	}
	if src.EditRate.Num%src.EditRate.Den != 0 {
		return nil, nil, NewErr(StageTimeline, src.Name, src.OffsetOf("edit_rate"),
			"non-integer edit rate %d/%d unsupported", src.EditRate.Num, src.EditRate.Den)
	}
	scale := src.EditRate.Num / src.EditRate.Den

	// 1) 旋转归一化到 0/90/180/270。
	rot := ((src.Rotation % 360) + 360) % 360
	if rot%90 != 0 {
		return nil, nil, NewErr(StageTimeline, src.Name, src.OffsetOf("rotation"),
			"unsupported rotation %d", src.Rotation)
	}
	w, h := src.Width, src.Height
	if rot == 90 || rot == 270 {
		w, h = h, w
	}
	ct.Transform.Rotation = rot

	// 2) 裁剪在旋转后的坐标系内校验。
	if src.Crop != nil {
		cr := src.Crop
		if cr.X < 0 || cr.Y < 0 || cr.W <= 0 || cr.H <= 0 ||
			cr.X+cr.W > w || cr.Y+cr.H > h {
			return nil, nil, NewErr(StageTimeline, src.Name, src.OffsetOf("crop"),
				"crop %dx%d+%d+%d out of bounds %dx%d",
				cr.W, cr.H, cr.X, cr.Y, w, h)
		}
		c := *cr
		ct.Transform.Crop = &c
		w, h = cr.W, cr.H
	}
	ct.Transform.Width, ct.Transform.Height = w, h

	// 3) 色彩冲突裁决。
	color, decs := ResolveColor(src.Name, []ColorInfo{src.Color, src.AltColor}, opts.ColorPriority)
	ct.Color = color

	// 4) 样本时间换算到主时基：非零起点平移、舍入确定、禁止倒退。
	var cum int64
	var prev int64 = -1
	for _, s := range src.Samples {
		if s.Duration < 0 {
			return nil, nil, NewErr(StageTimeline, src.Name, s.Offset,
				"negative sample duration %d at index %d", s.Duration, s.Index)
		}
		t := Convert(cum+src.Origin, scale, MasterScale)
		if prev >= 0 && t < prev {
			return nil, nil, NewErr(StageTimeline, src.Name, s.Offset,
				"output time regression at sample %d: %d < %d", s.Index, t, prev)
		}
		prev = t
		ct.Samples = append(ct.Samples, ComposedSample{
			Index:      s.Index,
			Time:       Time{Units: t, Scale: MasterScale},
			Duration:   Time{Units: Convert(s.Duration, scale, MasterScale), Scale: MasterScale},
			TrackUnits: cum + src.Origin,
			Size:       s.Size,
			Offset:     s.Offset,
		})
		cum += s.Duration
	}
	return ct, decs, nil
}

// VideoSamples 返回全部视频输出样本（按轨道顺序）。
func (tl *Timeline) VideoSamples() []ComposedSample {
	var out []ComposedSample
	for _, tr := range tl.Tracks {
		if tr.Meta.Kind == KindVideo {
			out = append(out, tr.Samples...)
		}
	}
	return out
}

// TotalSampleBytes 统计所有轨道的样本总字节数。
func (tl *Timeline) TotalSampleBytes() int64 {
	var n int64
	for _, tr := range tl.Tracks {
		for _, s := range tr.Samples {
			n += s.Size
		}
	}
	return n
}

// DurationRat 返回时间线时长（各轨样本时长之和的最大值，有理数秒）。
func (tl *Timeline) DurationRat() Rat {
	var best Rat
	for _, tr := range tl.Tracks {
		var sum Rat
		for _, s := range tr.Samples {
			sum = sum.Add(s.Duration.Rat())
		}
		if sum.Cmp(best) > 0 {
			best = sum
		}
	}
	return best
}

// TrackName 返回第 i 轨的稳定名称。
func TrackName(prefix string, id int) string {
	return prefix + "-" + strconv.Itoa(id)
}
