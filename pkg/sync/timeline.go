// Package sync converts audio and video clocks to one rational microsecond
// timeline and measures explicit A/V offset and drift.
package sync

import (
	"sort"

	"github.com/zld/medcheck/pkg/media"
)

const (
	seq32Mod  = int64(1) << 32
	seq32Half = int64(1) << 31
	seq32Mask = seq32Mod - 1
)

// UnwrapTimestamp32 extends a wrapping 32-bit RTP timestamp.
func UnwrapTimestamp32(ts uint32, last int64, started bool) int64 {
	raw := int64(ts)
	if !started {
		return raw
	}
	candidate := (last &^ seq32Mask) | raw
	if candidate+seq32Half < last {
		candidate += seq32Mod
	}
	if candidate > last+seq32Half {
		candidate -= seq32Mod
	}
	return candidate
}

// TrackClock is one media clock mapped to the common microsecond base.
type TrackClock struct {
	TrackID   string         `json:"track_id"`
	TimeBase  media.Rational `json:"time_base"`
	OriginUS  int64          `json:"origin_us"`
	OffsetUS  int64          `json:"offset_us"`
	started   bool
	anchorExt int64
	lastExt   int64
}

// Point is one timestamp observation on the common timeline.
type Point struct {
	TrackID    string `json:"track_id"`
	Sequence   int64  `json:"sequence"`
	ByteOffset int64  `json:"byte_offset"`
	RawTS      uint32 `json:"raw_ts"`
	ExtTS      int64  `json:"ext_ts"`
	NominalUS  int64  `json:"nominal_us"`
	OffsetUS   int64  `json:"applied_offset_us"`
	PTSUS      int64  `json:"pts_us"`
	Anomaly    bool   `json:"anomaly"`
}

// Timeline owns per-track clocks and shared timestamp issues.
type Timeline struct {
	Clocks map[string]*TrackClock `json:"clocks"`
	Points []Point                `json:"points"`
	Issues media.Issues           `json:"issues"`
}

// NewTimeline creates an empty timeline.
func NewTimeline() *Timeline {
	return &Timeline{Clocks: map[string]*TrackClock{}}
}

// AddClock registers a track clock.
func (tl *Timeline) AddClock(c TrackClock) error {
	if err := c.TimeBase.Validate(); err != nil {
		return err
	}
	cc := c
	tl.Clocks[c.TrackID] = &cc
	return nil
}

// Map converts one raw RTP timestamp and records wrap/monotonicity anomalies.
func (tl *Timeline) Map(trackID string, rawTS uint32, sequence, byteOffset int64) (Point, error) {
	c, ok := tl.Clocks[trackID]
	if !ok {
		return Point{}, media.NewError(media.CatInvalidArgument, trackID, sequence, byteOffset, "unknown timeline clock")
	}
	ext := UnwrapTimestamp32(rawTS, c.lastExt, c.started)
	p := Point{TrackID: trackID, Sequence: sequence, ByteOffset: byteOffset, RawTS: rawTS, ExtTS: ext, OffsetUS: c.OffsetUS}
	if !c.started {
		c.started = true
		c.anchorExt = ext
		c.lastExt = ext
	} else {
		if ext < c.lastExt {
			p.Anomaly = true
			tl.Issues.Add(media.CatTimestampAnomaly, trackID, sequence, byteOffset,
				"timestamp moved backwards: ext=%d last=%d", ext, c.lastExt)
		}
		c.lastExt = ext
	}
	us, err := media.Convert(ext-c.anchorExt, c.TimeBase, media.Microsecond)
	if err != nil {
		return Point{}, err
	}
	p.NominalUS = c.OriginUS + us
	p.PTSUS = p.NominalUS + c.OffsetUS
	tl.Points = append(tl.Points, p)
	return p, nil
}

// AlignmentPoint is a nearest-time audio/video correlation.
type AlignmentPoint struct {
	Ordinal    int   `json:"ordinal"`
	VideoSeq   int64 `json:"video_sequence"`
	AudioSeq   int64 `json:"audio_sequence"`
	VideoUS    int64 `json:"video_us"`
	AudioUS    int64 `json:"audio_us"`
	DeltaUS    int64 `json:"av_delta_us"`
	ResidualUS int64 `json:"residual_us"`
}

// Alignment quantifies offset and drift for two point sets.
type Alignment struct {
	VideoTrack  string           `json:"video_track"`
	AudioTrack  string           `json:"audio_track"`
	AVOffsetUS  int64            `json:"av_offset_us"`
	DriftUS     int64            `json:"drift_us"`
	MaxAVDiffUS int64            `json:"max_av_diff_us"`
	Points      []AlignmentPoint `json:"points"`
}

// Align correlates every video point with its nearest audio point. Offset is
// the first A/V delta; drift is the maximum residual from that offset.
func Align(videoTrack, audioTrack string, video, audio []Point) (*Alignment, error) {
	if len(video) == 0 || len(audio) == 0 {
		return nil, media.NewError(media.CatInvalidArgument, videoTrack, -1, -1, "cannot align empty track")
	}
	a := &Alignment{VideoTrack: videoTrack, AudioTrack: audioTrack}
	for i, v := range video {
		j := nearestAudio(v.PTSUS, audio)
		au := audio[j]
		delta := au.PTSUS - v.PTSUS
		if i == 0 {
			a.AVOffsetUS = delta
		}
		residual := delta - a.AVOffsetUS
		a.DriftUS = maxInt64(a.DriftUS, abs64(residual))
		a.MaxAVDiffUS = maxInt64(a.MaxAVDiffUS, abs64(delta))
		a.Points = append(a.Points, AlignmentPoint{
			Ordinal: i, VideoSeq: v.Sequence, AudioSeq: au.Sequence,
			VideoUS: v.PTSUS, AudioUS: au.PTSUS, DeltaUS: delta, ResidualUS: residual,
		})
	}
	return a, nil
}

func nearestAudio(us int64, audio []Point) int {
	j := sort.Search(len(audio), func(i int) bool { return audio[i].PTSUS >= us })
	if j == 0 {
		return 0
	}
	if j >= len(audio) {
		return len(audio) - 1
	}
	if abs64(audio[j-1].PTSUS-us) <= abs64(audio[j].PTSUS-us) {
		return j - 1
	}
	return j
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
