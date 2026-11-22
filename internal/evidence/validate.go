package evidence

import (
	"mq/internal/timeline"
)

// AlignReport 汇总帧时间与清单时间的对齐误差。
type AlignReport struct {
	MaxMs  float64
	MeanMs float64
}

// CheckAlignment 逐项比对帧时间与清单时间，报告最大及平均对齐误差。
// 帧数与清单条目数不一致时给出缺帧定位错误。
func CheckAlignment(frames []FrameSummary, entries []Entry) (AlignReport, error) {
	var rep AlignReport
	if len(frames) != len(entries) {
		return rep, timeline.NewErr(timeline.StageValidate, "", -1,
			"missing frames: %d frames vs %d manifest entries",
			len(frames), len(entries))
	}
	var sum float64
	for i := range frames {
		d := frames[i].Time.Rat().Sub(entries[i].TimelineTime.Rat()).Abs()
		ms := d.Milli()
		if ms > rep.MaxMs {
			rep.MaxMs = ms
		}
		sum += ms
	}
	if len(frames) > 0 {
		rep.MeanMs = sum / float64(len(frames))
	}
	return rep, nil
}

// BitrateReport 为码率偏差报告。
type BitrateReport struct {
	ActualBps int64
	DeltaPct  float64
}

// CheckBitrate 以目标码率和样本时长计算实际码率偏差。
func CheckBitrate(totalBytes int64, duration timeline.Rat, targetBps int64) BitrateReport {
	var rep BitrateReport
	if duration.Num <= 0 || targetBps <= 0 {
		return rep
	}
	actual := timeline.NewRat(totalBytes*8, 1).Div(duration)
	rep.ActualBps = actual.Num / actual.Den
	rep.DeltaPct = (float64(rep.ActualBps) - float64(targetBps)) / float64(targetBps) * 100
	return rep
}

// CheckFrames 检测缺帧与重复时间戳，分别给出定位错误。
func CheckFrames(frames []FrameSummary) error {
	var prev *FrameSummary
	for i := range frames {
		f := &frames[i]
		if f.Index != i {
			return timeline.NewErr(timeline.StageValidate, "", -1,
				"missing frame at position %d (got index %d)", i, f.Index)
		}
		if prev != nil && f.Time.Rat().Cmp(prev.Time.Rat()) == 0 {
			return timeline.NewErr(timeline.StageValidate, "", -1,
				"duplicate timestamp at frame %d: %s", i, f.Time.String())
		}
		prev = f
	}
	return nil
}

// CheckTransform 在验证阶段复核几何不变量（裁剪越界的第二道防线）。
func CheckTransform(tl *timeline.Timeline) error {
	for ti := range tl.Tracks {
		tr := &tl.Tracks[ti]
		t := tr.Transform
		if t.Width <= 0 || t.Height <= 0 {
			return timeline.NewErr(timeline.StageValidate, tr.Meta.Name, -1,
				"non-positive frame geometry %dx%d", t.Width, t.Height)
		}
		if t.Crop != nil {
			c := t.Crop
			if c.X < 0 || c.Y < 0 || c.W <= 0 || c.H <= 0 ||
				c.X+c.W > tr.Meta.Width || c.Y+c.H > tr.Meta.Height {
				return timeline.NewErr(timeline.StageValidate, tr.Meta.Name,
					tr.Meta.OffsetOf("crop"),
					"crop %dx%d+%d+%d out of bounds %dx%d",
					c.W, c.H, c.X, c.Y, tr.Meta.Width, tr.Meta.Height)
			}
		}
	}
	return nil
}

// QualityReport 汇总帧序列质量统计。
type QualityReport struct {
	Mean float64 // 全部帧平均亮度的均值（0..1）
	Min  float64 // 最低帧平均亮度（0..1）
}

// CheckQuality 统计帧序列亮度质量。
func CheckQuality(frames []FrameSummary) QualityReport {
	var rep QualityReport
	if len(frames) == 0 {
		return rep
	}
	rep.Min = 1
	var sum float64
	for _, f := range frames {
		q := f.Mean / 255
		sum += q
		if q < rep.Min {
			rep.Min = q
		}
	}
	rep.Mean = sum / float64(len(frames))
	return rep
}
