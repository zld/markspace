package pipeline

import (
	"github.com/zld/mediapipe/index"
	"github.com/zld/mediapipe/media"
)

// SyncToleranceMS is the end-to-end synchronization threshold: inputs whose
// audio/video start, end or intermediate samples differ by more than this
// fail the check.
const SyncToleranceMS = 40

// SyncTolerance is SyncToleranceMS as media time.
var SyncTolerance = media.Time(SyncToleranceMS) * media.Time(1_000_000)

// SyncReport is the end-to-end synchronization analysis for one input.
type SyncReport struct {
	VideoStartUS      int64         `json:"video_start_us"`
	VideoEndUS        int64         `json:"video_end_us"`
	AudioStartUS      int64         `json:"audio_start_us"`
	AudioEndUS        int64         `json:"audio_end_us"`
	StartErrorUS      int64         `json:"start_error_us"`
	EndErrorUS        int64         `json:"end_error_us"`
	MaxErrorUS        int64         `json:"max_error_us"`
	AtUS              int64         `json:"at_us"`
	IntermediateNodes int           `json:"intermediate_nodes"`
	ToleranceUS       int64         `json:"tolerance_us"`
	OK                bool          `json:"ok"`
	Issues            []media.Issue `json:"issues"`
}

// Iissue is a placeholder alias kept out of the public API.
type Iissue = media.Issue

// CheckSync compares the audio and video timelines of one container.
//
// It checks the first and last sample of each track and, for every video
// sample, the nearest audio sample in time. The largest absolute difference is
// the end-to-end sync error; exceeding SyncTolerance fails the check. A
// container with fewer than two media tracks has nothing to compare and is
// reported as OK with zero error.
func CheckSync(video, audio *index.TrackIndex) SyncReport {
	rep := SyncReport{ToleranceUS: SyncTolerance.Micros()}
	if video == nil || audio == nil || len(video.Samples) == 0 || len(audio.Samples) == 0 {
		rep.OK = true
		return rep
	}
	v := video.Samples
	a := audio.Samples
	rep.VideoStartUS = v[0].StartUS
	rep.VideoEndUS = v[len(v)-1].StartUS + v[len(v)-1].DurUS
	rep.AudioStartUS = a[0].StartUS
	rep.AudioEndUS = a[len(a)-1].StartUS + a[len(a)-1].DurUS
	rep.StartErrorUS = rep.VideoStartUS - rep.AudioStartUS
	rep.EndErrorUS = rep.VideoEndUS - rep.AudioEndUS

	maxErr := abs64(rep.StartErrorUS)
	maxAt := int64(0)
	if e := abs64(rep.EndErrorUS); e > maxErr {
		maxErr, maxAt = e, rep.VideoEndUS
	}

	// Intermediate sampling: for each video start, walk the audio samples to
	// the nearest one. Audio sample starts are monotonic, so a single cursor
	// suffices and the scan is linear.
	cursor := 0
	for _, vs := range v {
		for cursor+1 < len(a) && abs64(a[cursor+1].StartUS-vs.StartUS) < abs64(a[cursor].StartUS-vs.StartUS) {
			cursor++
		}
		diff := vs.StartUS - a[cursor].StartUS
		rep.IntermediateNodes++
		if e := abs64(diff); e > maxErr {
			maxErr, maxAt = e, vs.StartUS
		}
	}

	rep.MaxErrorUS = maxErr
	rep.AtUS = maxAt
	rep.OK = true
	if maxErr > rep.ToleranceUS {
		rep.OK = false
		rep.Issues = append(rep.Issues, media.Issue{
			Kind: media.KindSyncTolerance, Severity: media.SeverityError,
			Detail: media.NewError(media.KindSyncTolerance, "",
				"audio/video offset %dus exceeds %dus tolerance", maxErr, rep.ToleranceUS).Error(),
			TimeUS: maxAt,
		})
	}
	return rep
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
