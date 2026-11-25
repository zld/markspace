package probe

import (
	"math"
	"os"
	"strconv"

	"mediaflow/internal/fault"
	"mediaflow/internal/policy"
)

// ScanFile parses boxes, tracks and sample tables and returns all located findings.
// OS-level open/digest failures return a classified error; malformed media is represented
// on the returned Report so batch processing can preserve per-item conclusions.
func ScanFile(path string) (*Report, error) {
	digest, size, err := DigestFile(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Wrap(fault.ClassIO, err, "open media for scan")
	}
	defer f.Close()

	root, intervals := scanBoxTree(f, 0, size)
	libCount, libErr := libraryInspect(path)

	r := &Report{
		Path: path, FileSize: size, InputDigest: digest,
		LibraryBoxCount: libCount, Intervals: intervals,
	}
	moov := findTopBox(root, "moov")
	if moov == nil {
		if !hasRejectClass(r.Intervals) {
			r.Intervals = append(r.Intervals, fault.Interval{
				Kind: "missing_moov", Class: fault.ClassMissingTrack,
				ByteStart: 0, ByteEnd: size, Reject: true, Quarantine: true,
				Message: "no movie box found",
			})
		}
	} else {
		tracks, trackFaults := parseTracks(f, moov)
		r.Tracks = tracks
		r.Intervals = append(r.Intervals, trackFaults...)
	}
	if libErr != nil && !r.HasRejection() {
		r.Intervals = append(r.Intervals, fault.Interval{
			Kind: "library_box_warning", Class: fault.ClassUnsupported,
			ByteStart: 0, ByteEnd: size, Reject: true, Quarantine: true,
			Message: "go-mp4 could not parse box structure: " + libErr.Error(),
		})
	}
	r.Stats = BuildStats(r)
	r.Supported = !r.HasRejection()
	return r, nil
}

func findTopBox(nodes []*boxNode, typ string) *boxNode {
	for _, n := range nodes {
		if n.typ == typ {
			return n
		}
	}
	return nil
}

func hasRejectClass(in []fault.Interval) bool {
	for _, x := range in {
		if x.Reject {
			return true
		}
	}
	return false
}

// BuildStats aggregates reproducible stream statistics for policy evaluation.
func BuildStats(r *Report) policy.Stats {
	s := policy.Stats{ContainerBytes: r.FileSize}
	var video *Track
	for _, t := range r.Tracks {
		if t == nil {
			continue
		}
		if ms, err := t.Timeline.MediaTimebase().TicksToMS(t.LastTick); err == nil && ms > s.DurationMS {
			s.DurationMS = ms
		}
		for _, sm := range t.Samples {
			if t.Type == "video" {
				s.VideoBytes += int64(sm.Size)
			} else if t.Type == "audio" {
				s.AudioBytes += int64(sm.Size)
			}
		}
		if t.Type == "video" && video == nil {
			video = t
		}
	}
	if video != nil {
		s.Width = int(video.Width)
		s.Height = int(video.Height)
		s.SampleCount = len(video.Samples)
		s.KeyframeCount = len(video.SyncSamples)
		s.MaxKeyframeGapMS = MaxKeyframeGapMS(video)
		if s.DurationMS > 0 {
			s.FPS = int(math.Round(float64(s.SampleCount) * 1000 / float64(s.DurationMS)))
		}
	}
	for _, in := range r.Intervals {
		if in.Class == fault.ClassNonZeroStart {
			s.AnomalyPenalty += 0.04
		}
	}
	if s.AnomalyPenalty > 0.2 {
		s.AnomalyPenalty = 0.2
	}
	return s
}

// ValidateExpectations checks manifest track, timebase and dimension constraints.
func ValidateExpectations(it Item, r *Report) []fault.Interval {
	var out []fault.Interval
	for _, req := range it.RequiredTracks {
		t := r.FindTrack(req.ID)
		if t == nil {
			out = append(out, fault.Interval{
				Kind: "missing_required_track", Class: fault.ClassMissingTrack,
				TrackID: req.ID, Reject: true,
				Message: "required track is absent: expected " + req.Type,
			})
			continue
		}
		if req.Type != "" && t.Type != req.Type {
			out = append(out, fault.Interval{
				Kind: "track_type_mismatch", Class: fault.ClassUnsupported,
				TrackID: req.ID, TrackNumber: t.Number, Reject: true,
				Message: "track type " + t.Type + " does not match expected " + req.Type,
			})
		}
		if req.Timescale != 0 && t.Timescale != req.Timescale {
			startMS, _ := t.Timeline.AbsoluteMS(0)
			out = append(out, fault.Interval{
				Kind: "timebase_mismatch", Class: fault.ClassTimeBase,
				TrackID: req.ID, TrackNumber: t.Number, StartMS: startMS, Reject: true,
				Message: "timescale " + mathString(t.Timescale) + " does not match expected " + mathString(req.Timescale),
			})
		}
	}
	if it.Width > 0 || it.Height > 0 || it.FPS > 0 {
		for _, t := range r.Tracks {
			if t == nil || t.Type != "video" {
				continue
			}
			if it.Width > 0 && int(t.Width) != it.Width {
				out = append(out, constraintInterval(t, "width", float64(it.Width), float64(t.Width)))
			}
			if it.Height > 0 && int(t.Height) != it.Height {
				out = append(out, constraintInterval(t, "height", float64(it.Height), float64(t.Height)))
			}
			if it.FPS > 0 && r.Stats.FPS > 0 && intAbs(r.Stats.FPS-it.FPS) > 1 {
				out = append(out, constraintInterval(t, "fps", float64(it.FPS), float64(r.Stats.FPS)))
			}
		}
	}
	return out
}

func constraintInterval(t *Track, name string, expected, actual float64) fault.Interval {
	return fault.Interval{
		Kind: "constraint_mismatch", Class: fault.ClassManifest,
		TrackID: t.ID, TrackNumber: t.Number, Reject: true,
		Message: name + " does not match expected constraint",
	}
}

func mathString(v uint32) string {
	return strconv.FormatUint(uint64(v), 10)
}

func intAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
