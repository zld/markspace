package evidence

import (
	"fmt"
	"strings"

	"mq/internal/timeline"
)

// Entry 为清单条目：帧序号、时间线时间、轨道时间与源偏移。
type Entry struct {
	Index        int
	TimelineTime timeline.Time // 主时基
	TrackTime    timeline.Time // 轨道时基
	SourceOffset int64
}

// BuildManifest 由合成时间线生成逐帧时间清单。
func BuildManifest(tl *timeline.Timeline) []Entry {
	var entries []Entry
	idx := 0
	for ti := range tl.Tracks {
		tr := &tl.Tracks[ti]
		if tr.Meta.Kind != timeline.KindVideo {
			continue
		}
		scale := tr.Meta.EditRate.Num / tr.Meta.EditRate.Den
		for _, s := range tr.Samples {
			entries = append(entries, Entry{
				Index:        idx,
				TimelineTime: s.Time,
				TrackTime:    timeline.Time{Units: s.TrackUnits, Scale: scale},
				SourceOffset: s.Offset,
			})
			idx++
		}
	}
	return entries
}

// String 返回清单条目的确定性文本行。
func (e Entry) String() string {
	return fmt.Sprintf("frame=%d timeline=%d/%d track=%d/%d offset=%d",
		e.Index, e.TimelineTime.Units, e.TimelineTime.Scale,
		e.TrackTime.Units, e.TrackTime.Scale, e.SourceOffset)
}

// MarshalManifest 将清单序列化为确定性文本。
func MarshalManifest(entries []Entry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.String())
		b.WriteByte('\n')
	}
	return b.String()
}
