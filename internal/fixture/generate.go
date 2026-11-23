// Package fixture generates deterministic RTP/RTCP captures and fault
// samples entirely from code. No fixture is read from outside the repository,
// and the same seed always yields byte-identical input.
package fixture

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"

	"mediatool/internal/clock"
	"mediatool/internal/rtp"
	"mediatool/internal/video"
)

// EpochNs is the fixed NTP instant used as the media epoch for fixtures.
// It is deliberately not wall-clock time so runs are reproducible.
const EpochNs int64 = 1_700_000_000 * 1e9

// TrackSpec describes one source track in a generated capture.
type TrackSpec struct {
	TrackID    uint32
	SSRC       uint32
	Kind       string // "audio" or "video"
	ClockRate  uint32
	Layout     string
	SampleRate int
	Width      int
	Height     int
	// IntervalMs is the video frame interval or audio packet interval.
	IntervalMs int64
	Payload    int
	// ColorCandidates are the conflicting color descriptions of a video track.
	ColorCandidates []video.Candidate
}

// Options controls capture generation.
type Options struct {
	Seed         int64
	Tracks       []TrackSpec
	DurationMs   int64
	SegmentMs    int64
	SRIntervalMs int64

	InjectLoss      int
	InjectTruncate  int
	InjectReorder   int
	InjectDuplicate int

	// InjectRTCPRegression adds one non-monotonic sender report for the named
	// track at the given media time. It is off by default so the standard demo
	// keeps a fully trustworthy timeline.
	InjectRTCPRegression bool
	RTCPRegressionTrack  uint32
	RTCPRegressionAtMs   int64
}

// Capture is a generated stream plus the ground truth needed to verify that
// faults were located correctly.
type Capture struct {
	Bytes []byte
	// TruncationOffsets are the true first suspicious byte offsets.
	TruncationOffsets []int64
	// LossSeqs lists the sequence numbers deliberately omitted, per track.
	LossSeqs map[uint32][]uint16
	// Injected totals.
	LossInjected    int
	Truncations     int
	Reorders        int
	Duplicates      int
	RTCPRegressions int
	FirstTruncation int64
}

type event struct {
	timeMs   int64
	priority int // 0 = sender report, 1 = media packet
	trackID  uint32
	ssrc     uint32
	sr       bool
	srNTP    clock.NTP
	srRTP    uint32
	seq      uint16
	ts       uint32
	keyframe bool
	payload  []byte
	// truncateTo, when positive, is how many body bytes survive.
	truncateTo int
	loss       bool
	dupOf      *event
}

// DemoOptions builds the standard four-track fixture used by the demo:
// two video tracks and two audio tracks, with every fault class represented.
func DemoOptions(seed int64, tracks, bitrates, loss, trunc int) Options {
	_ = bitrates // the ladder is a packaging parameter, not a capture one
	specs := make([]TrackSpec, 0, tracks)
	base := []TrackSpec{
		{TrackID: 1, SSRC: 0x1111, Kind: "video", ClockRate: 90000, Width: 1920, Height: 1080,
			IntervalMs: 500, Payload: 2048,
			ColorCandidates: []video.Candidate{
				{Source: "container", Priority: 0, Color: video.Color{Primaries: 1, Transfer: 1, Matrix: 1, Known: true}, Offset: 1},
				{Source: "sidecar:cam-b", Priority: 1, Color: video.Color{Primaries: 9, Transfer: 16, Matrix: 9, Known: true}, Offset: 2},
			}},
		{TrackID: 2, SSRC: 0x2222, Kind: "video", ClockRate: 90000, Width: 1280, Height: 720,
			IntervalMs: 500, Payload: 1536,
			ColorCandidates: []video.Candidate{
				{Source: "sidecar:cam-a", Priority: 2, Color: video.Color{Primaries: 1, Transfer: 1, Matrix: 1, Known: true}, Offset: 3},
			}},
		{TrackID: 3, SSRC: 0x3333, Kind: "audio", ClockRate: 48000, Layout: "5.1", SampleRate: 48000,
			IntervalMs: 20, Payload: 384},
		{TrackID: 4, SSRC: 0x4444, Kind: "audio", ClockRate: 48000, Layout: "stereo", SampleRate: 48000,
			IntervalMs: 20, Payload: 320},
	}
	for i := 0; i < tracks && i < len(base); i++ {
		specs = append(specs, base[i])
	}
	// Additional tracks repeat the audio shape with derived identifiers.
	for i := len(base); i < tracks; i++ {
		specs = append(specs, TrackSpec{
			TrackID: uint32(i + 1), SSRC: uint32(0x5000 + i), Kind: "audio",
			ClockRate: 48000, Layout: "stereo", SampleRate: 48000,
			IntervalMs: 20, Payload: 320,
		})
	}
	for i := range specs {
		if specs[i].SSRC == 0 {
			specs[i].SSRC = uint32(0x5000 + i)
		}
	}
	return Options{
		Seed:            seed,
		Tracks:          specs,
		DurationMs:      8000,
		SegmentMs:       1000,
		SRIntervalMs:    500,
		InjectLoss:      loss,
		InjectTruncate:  trunc,
		InjectReorder:   2,
		InjectDuplicate: 1,
	}
}

// Generate builds a capture in memory.
func Generate(opts Options) (*Capture, error) {
	if len(opts.Tracks) == 0 {
		return nil, fmt.Errorf("fixture: no tracks")
	}
	if opts.DurationMs <= 0 {
		return nil, fmt.Errorf("fixture: duration must be positive")
	}
	if opts.SegmentMs <= 0 {
		opts.SegmentMs = 1000
	}
	if opts.SRIntervalMs <= 0 {
		opts.SRIntervalMs = 500
	}
	rng := rand.New(rand.NewSource(opts.Seed))
	cap := &Capture{LossSeqs: map[uint32][]uint16{}}

	var events []*event
	for _, spec := range opts.Tracks {
		events = append(events, trackEvents(opts, spec, rng)...)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].timeMs != events[j].timeMs {
			return events[i].timeMs < events[j].timeMs
		}
		if events[i].priority != events[j].priority {
			return events[i].priority < events[j].priority
		}
		if events[i].trackID != events[j].trackID {
			return events[i].trackID < events[j].trackID
		}
		return events[i].seq < events[j].seq
	})

	packets := make([]*event, 0, len(events))
	for _, ev := range events {
		if !ev.sr {
			packets = append(packets, ev)
		}
	}
	if len(packets) == 0 {
		return nil, fmt.Errorf("fixture: no media packets")
	}

	// Losses: prefer audio packets so keyframe grids stay intact.
	audio := make([]*event, 0, len(packets))
	other := make([]*event, 0, len(packets))
	for _, ev := range packets {
		if ev.keyframe {
			other = append(other, ev)
		} else {
			audio = append(audio, ev)
		}
	}
	candidates := append(audio, other...)
	if opts.InjectLoss > len(candidates) {
		opts.InjectLoss = len(candidates)
	}
	used := map[*event]bool{}
	for i := 0; i < opts.InjectLoss; i++ {
		idx := rng.Intn(len(candidates))
		ev := candidates[idx]
		if used[ev] || ev.keyframe && ev.trackID <= 2 {
			i--
			continue
		}
		used[ev] = true
		ev.loss = true
		cap.LossSeqs[ev.trackID] = append(cap.LossSeqs[ev.trackID], ev.seq)
		cap.LossInjected++
	}

	// Truncation: later audio packets so the damaged segment is well inside
	// the stream and can be used to test checkpoint rollback.
	truncCandidates := make([]*event, 0, len(audio))
	for _, ev := range audio {
		if !ev.loss && ev.timeMs >= opts.DurationMs/2 {
			truncCandidates = append(truncCandidates, ev)
		}
	}
	if len(truncCandidates) == 0 {
		truncCandidates = audio
	}
	for i := 0; i < opts.InjectTruncate && len(truncCandidates) > 0; i++ {
		ev := truncCandidates[rng.Intn(len(truncCandidates))]
		if ev.truncateTo > 0 {
			continue
		}
		ev.truncateTo = 16 // RTP header plus four payload bytes
		cap.Truncations++
	}

	// Reorder: swap two adjacent media packets of the same track so a later
	// sequence number is delivered first. Sender reports keep their position.
	live := liveEvents(events)
	swapped := 0
	for i := 0; i+1 < len(live) && swapped < opts.InjectReorder; i++ {
		j := i + 1
		for j < len(live) && live[j].sr {
			j++
		}
		if j >= len(live) {
			break
		}
		a, b := live[i], live[j]
		if a.loss || b.loss || a.sr || a.trackID != b.trackID || a.seq+1 != b.seq {
			continue
		}
		live[i], live[j] = b, a
		cap.Reorders++
		swapped++
		i = j
	}
	// Duplicate: insert a copy after a surviving packet.
	dupCount := 0
	for i := 0; i < len(live) && dupCount < opts.InjectDuplicate; i++ {
		ev := live[i]
		if ev.loss || ev.sr || ev.truncateTo > 0 {
			continue
		}
		cp := *ev
		cp.dupOf = ev
		live = append(live[:i+1], append([]*event{&cp}, live[i+1:]...)...)
		cap.Duplicates++
		dupCount++
		i++
	}

	// Serialize.
	out := append([]byte(nil), rtp.Magic...)
	for _, ev := range live {
		if ev.loss {
			continue
		}
		body := buildBody(opts, ev)
		declared := len(body)
		captured := body
		if ev.truncateTo > 0 && ev.truncateTo < len(body) {
			captured = body[:ev.truncateTo]
			cap.TruncationOffsets = append(cap.TruncationOffsets, int64(len(out))+8)
		}
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(len(captured)))
		binary.BigEndian.PutUint32(hdr[4:8], uint32(declared))
		out = append(out, hdr[:]...)
		out = append(out, captured...)
	}
	cap.Bytes = out
	if len(cap.TruncationOffsets) > 0 {
		cap.FirstTruncation = cap.TruncationOffsets[0]
	}
	if opts.InjectRTCPRegression {
		cap.RTCPRegressions = 1
	}
	return cap, nil
}

func liveEvents(events []*event) []*event {
	out := make([]*event, len(events))
	copy(out, events)
	return out
}

func trackEvents(opts Options, spec TrackSpec, rng *rand.Rand) []*event {
	var out []*event
	tsStart := uint32(rng.Intn(1 << 20))
	seq := uint16(rng.Intn(1 << 12))
	interval := spec.IntervalMs
	if interval <= 0 {
		interval = 20
	}
	for t := int64(0); t < opts.DurationMs; t += interval {
		keyframe := false
		if spec.Kind == "video" {
			keyframe = t%opts.SegmentMs == 0
		} else {
			// Audio packets are independently decodable and therefore usable
			// as alignment anchors.
			keyframe = true
		}
		pl := spec.Payload
		if pl <= 0 {
			pl = 128
		}
		body := expand([]byte(fmt.Sprintf("%d/%d/%d/%d", opts.Seed, spec.TrackID, t, pl)), pl)
		if spec.Kind == "video" {
			if keyframe {
				body[0] = 1
			} else {
				body[0] = 0
			}
		}
		out = append(out, &event{
			timeMs:   t,
			priority: 1,
			trackID:  spec.TrackID,
			ssrc:     spec.SSRC,
			seq:      seq,
			ts:       tsStart + uint32(t*int64(spec.ClockRate)/1000),
			keyframe: keyframe,
			payload:  body,
		})
		seq++
	}
	// Sender reports every SRIntervalMs, with one optional regression.
	for t := int64(0); t <= opts.DurationMs; t += opts.SRIntervalMs {
		ntpMs := t
		rtpAt := func(ms int64) uint32 { return tsStart + uint32(ms*int64(spec.ClockRate)/1000) }
		if opts.InjectRTCPRegression && spec.TrackID == opts.RTCPRegressionTrack && t == opts.RTCPRegressionAtMs {
			// Step two report intervals back: a genuine, non-monotonic time.
			ntpMs = t - 2*opts.SRIntervalMs
		}
		sr := &event{
			timeMs:   t,
			priority: 0,
			trackID:  spec.TrackID,
			ssrc:     spec.SSRC,
			sr:       true,
			srNTP:    clock.NsToNTP(EpochNs + ntpMs*1e6),
			srRTP:    rtpAt(ntpMs),
		}
		out = append(out, sr)
	}
	return out
}

func buildBody(opts Options, ev *event) []byte {
	if ev.sr {
		return rtp.AppendSenderReport(nil, clock.SenderReport{
			TrackID: ev.trackID, SSRC: ev.ssrc, NTP: ev.srNTP, RTP: ev.srRTP,
			ClockRate: 0, PacketCount: 0, OctetCount: 0,
		})
	}
	body := make([]byte, 12+len(ev.payload))
	body[0] = 0x80
	body[1] = 96
	binary.BigEndian.PutUint16(body[2:4], ev.seq)
	binary.BigEndian.PutUint32(body[4:8], ev.ts)
	binary.BigEndian.PutUint32(body[8:12], ev.ssrc)
	copy(body[12:], ev.payload)
	return body
}

func expand(seed []byte, n int) []byte {
	out := make([]byte, 0, n+sha256.Size)
	var ctr [8]byte
	for i := 0; len(out) < n; i++ {
		binary.BigEndian.PutUint64(ctr[:], uint64(i))
		h := sha256.New()
		h.Write(seed)
		h.Write(ctr[:])
		out = append(out, h.Sum(nil)...)
	}
	return out[:n]
}
