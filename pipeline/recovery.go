// Package pipeline schedules multiple media tracks onto one unified timeline,
// enforces cross-stage invariants and turns transport-level loss into
// deterministic recovery events and checkpoints.
package pipeline

import (
	"mediacore/media"
)

// MaxAudioBurst is the largest consecutive audio packet loss the kernel will
// hide with synthetic silence.
const MaxAudioBurst = 3

// RecoveryEventKind enumerates what the recovery layer produced.
type RecoveryEventKind uint8

const (
	RecSilenceFill      RecoveryEventKind = iota // synthetic audio silence packet
	RecUnrecoveredAudio                          // audio gap beyond MaxAudioBurst
	RecVideoGap                                  // a video time hole was opened
	RecFrameSuppressed                           // non-key frame dropped while awaiting resync
	RecVideoResync                               // a key frame closed an open video hole
)

// RecoveryEvent is one deterministic recovery action on the unified timeline.
type RecoveryEvent struct {
	TrackID  uint32
	Kind     RecoveryEventKind
	SeqStart int64          // expanded sequence, inclusive
	SeqEnd   int64          // expanded sequence, exclusive
	When     media.Interval // unified-nanosecond span of the action
	Samples  int            // audio frames of silence (0 for video events)
}

// AudioFiller hides short audio packet bursts with deterministic silence. It
// never invents more than MaxAudioBurst packets per contiguous gap; anything
// longer is reported as an unrecovered audio gap.
type AudioFiller struct {
	trackID          uint32
	clockRate        uint32
	samplesPerPacket int
	maxBurst         int
	filled           int64
	unrecovered      int64
	lastSeq          int64
	haveLast         bool
}

// NewAudioFiller builds a filler. samplesPerPacket is the PCM frame count in
// one RTP audio packet, used to size silence and place it on the timeline.
func NewAudioFiller(trackID uint32, clockRate uint32, samplesPerPacket int) *AudioFiller {
	if samplesPerPacket <= 0 {
		samplesPerPacket = int(clockRate / 100) // 10 ms default
	}
	return &AudioFiller{
		trackID: trackID, clockRate: clockRate,
		samplesPerPacket: samplesPerPacket, maxBurst: MaxAudioBurst,
	}
}

// FillHole converts one sequence-number hole into recovery events. ptsFor
// maps an expanded sequence to its unified-nanosecond presentation time.
func (f *AudioFiller) FillHole(start, end int64, ptsFor func(seq int64) int64) []RecoveryEvent {
	var out []RecoveryEvent
	packetNanos, _ := media.ToNanos(int64(f.samplesPerPacket), media.TimeScale(f.clockRate))
	for seq := start; seq < end; seq++ {
		burst := seq - start
		pts := ptsFor(seq)
		iv := media.Interval{Start: pts, End: pts + packetNanos}
		if int(burst) >= f.maxBurst {
			out = append(out, RecoveryEvent{
				TrackID: f.trackID, Kind: RecUnrecoveredAudio,
				SeqStart: seq, SeqEnd: seq + 1, When: iv,
			})
			f.unrecovered++
			continue
		}
		out = append(out, RecoveryEvent{
			TrackID: f.trackID, Kind: RecSilenceFill,
			SeqStart: seq, SeqEnd: seq + 1, When: iv,
			Samples: f.samplesPerPacket,
		})
		f.filled++
	}
	f.lastSeq, f.haveLast = end-1, true
	return out
}

// Counters returns filled and unrecovered packet totals.
func (f *AudioFiller) Counters() (filled, unrecovered int64) {
	return f.filled, f.unrecovered
}

// VideoRecovery keeps a time hole open after video loss and discards every
// non-key frame until the next key frame, never fabricating picture.
type VideoRecovery struct {
	trackID    uint32
	waitingKey bool
	gaps       int64
	suppressed int64
	resyncs    int64
}

// NewVideoRecovery builds a video recovery tracker.
func NewVideoRecovery(trackID uint32) *VideoRecovery { return &VideoRecovery{trackID: trackID} }

// OpenHole marks that a sequence gap requires a key frame before normal
// frames may resume, returning the gap event.
func (v *VideoRecovery) OpenHole(start, end int64, when media.Interval) RecoveryEvent {
	v.waitingKey = true
	v.gaps++
	return RecoveryEvent{
		TrackID: v.trackID, Kind: RecVideoGap,
		SeqStart: start, SeqEnd: end, When: when,
	}
}

// AcceptFrame decides whether an arriving frame may be submitted. While a hole
// is open, non-key frames are suppressed; the first key frame resyncs.
func (v *VideoRecovery) AcceptFrame(isKey bool, seq int64, when media.Interval) (bool, []RecoveryEvent) {
	if !v.waitingKey {
		return true, nil
	}
	if isKey {
		v.waitingKey = false
		v.resyncs++
		return true, []RecoveryEvent{{
			TrackID: v.trackID, Kind: RecVideoResync,
			SeqStart: seq, SeqEnd: seq + 1, When: when,
		}}
	}
	v.suppressed++
	return false, []RecoveryEvent{{
		TrackID: v.trackID, Kind: RecFrameSuppressed,
		SeqStart: seq, SeqEnd: seq + 1, When: when,
	}}
}

// Counters returns gap, suppressed-frame and resync totals.
func (v *VideoRecovery) Counters() (gaps, suppressed, resyncs int64) {
	return v.gaps, v.suppressed, v.resyncs
}

// WaitingKey reports whether a hole is currently open.
func (v *VideoRecovery) WaitingKey() bool { return v.waitingKey }

// RecoveryState is the serialisable recovery checkpoint.
type RecoveryState struct {
	AudioFilled      int64 `json:"audio_filled"`
	AudioUnrecovered int64 `json:"audio_unrecovered"`
	VideoGaps        int64 `json:"video_gaps"`
	VideoSuppressed  int64 `json:"video_suppressed"`
	VideoResyncs     int64 `json:"video_resyncs"`
	WaitingKey       bool  `json:"waiting_key"`
}

// Snapshot captures recovery counters for both media kinds.
func (f *AudioFiller) Snapshot(v *VideoRecovery) RecoveryState {
	return RecoveryState{
		AudioFilled: f.filled, AudioUnrecovered: f.unrecovered,
		VideoGaps: v.gaps, VideoSuppressed: v.suppressed,
		VideoResyncs: v.resyncs, WaitingKey: v.waitingKey,
	}
}

// RestoreRecovery rebuilds recovery trackers from a checkpoint.
func RestoreRecovery(trackID uint32, clockRate uint32, samplesPerPacket int, s RecoveryState) (*AudioFiller, *VideoRecovery) {
	f := NewAudioFiller(trackID, clockRate, samplesPerPacket)
	f.filled, f.unrecovered = s.AudioFilled, s.AudioUnrecovered
	v := NewVideoRecovery(trackID)
	v.gaps, v.suppressed, v.resyncs = s.VideoGaps, s.VideoSuppressed, s.VideoResyncs
	v.waitingKey = s.WaitingKey
	return f, v
}
