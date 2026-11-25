package kernel

import "fmt"

// Sample is one normalized media access unit on the shared clock.
type Sample struct {
	Index       uint64 `json:"index"`
	PTS90k      uint64 `json:"pts_90k"`
	Duration90k uint64 `json:"duration_90k"`
	Flags       uint16 `json:"flags,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

// Time returns the sample's half-open shared-clock interval.
func (s Sample) Time() TimeRange {
	return TimeRange{Start90k: s.PTS90k, End90k: s.PTS90k + s.Duration90k}
}

// Track is a normalized audio, video, or event timeline.
type Track struct {
	ID       string    `json:"id"`
	PID      uint16    `json:"pid"`
	Kind     TrackKind `json:"kind"`
	Start90k uint64    `json:"start_90k"`
	End90k   uint64    `json:"end_90k"`
	Samples  []Sample  `json:"samples"`
}

// NormalizedEvent is a cut/key event on the shared clock.
type NormalizedEvent struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	PTS90k uint64 `json:"pts_90k"`
}

// NormalizedProgram is the common input to cutting, silence, fingerprints,
// checkpointing, and manifests.
type NormalizedProgram struct {
	SourceDigest string            `json:"source_digest"`
	Tracks       []*Track          `json:"tracks"`
	Events       []NormalizedEvent `json:"events"`
	Gaps         []GapEvent        `json:"gaps"`
	Wraps        int               `json:"wraps"`
	Duration90k  uint64            `json:"duration_90k"`
	InputPackets uint64            `json:"input_packets"`
}

// TrackBuilder constructs normalized tracks without mutating source bytes.
type TrackBuilder struct {
	program NormalizedProgram
	byPID   map[uint16]*Track
	lastPTS map[uint16]uint64
	havePTS map[uint16]bool
}

// NewTrackBuilder creates a builder for one source digest.
func NewTrackBuilder(sourceDigest string) *TrackBuilder {
	return &TrackBuilder{
		program: NormalizedProgram{SourceDigest: sourceDigest},
		byPID:   map[uint16]*Track{},
		lastPTS: map[uint16]uint64{},
		havePTS: map[uint16]bool{},
	}
}

// AddTSSPacket decodes a private fixture packet when its magic is present.
func (b *TrackBuilder) AddTSPacket(pkt TSPacket) error {
	if len(pkt.Payload) < len(wireMagic) {
		return nil
	}
	msg, err := DecodeMessage(pkt.Payload)
	if err != nil {
		// PSI and null packets do not use the private magic.
		return nil
	}
	return b.AddMessage(msg)
}

// AddMessage appends a wire message after shared-clock monotonicity checks.
func (b *TrackBuilder) AddMessage(msg WireMessage) error {
	if msg.IsEvent {
		b.program.Events = append(b.program.Events, NormalizedEvent{
			ID:     fmt.Sprintf("event-%d", len(b.program.Events)+1),
			Label:  msg.Label,
			PTS90k: msg.PTS90k,
		})
		b.updateDuration(msg.PTS90k + uint64(msg.Duration90k))
		return nil
	}

	if b.havePTS[msg.PID] && msg.PTS90k < b.lastPTS[msg.PID] {
		return &MediaError{
			Stage:       "tracks",
			Code:        "sample_timestamp_regression",
			Message:     fmt.Sprintf("PID 0x%04x sample PTS %d follows %d", msg.PID, msg.PTS90k, b.lastPTS[msg.PID]),
			PID:         msg.PID,
			Time:        TimeRange{Start90k: msg.PTS90k, End90k: b.lastPTS[msg.PID] + 1},
			Recoverable: false,
		}
	}

	track := b.track(msg.PID, msg.TrackKind)
	track.Samples = append(track.Samples, Sample{
		Index:       uint64(msg.Index),
		PTS90k:      msg.PTS90k,
		Duration90k: uint64(msg.Duration90k),
		Flags:       msg.Flags,
		Data:        append([]byte(nil), msg.Data...),
	})
	track.End90k = msg.PTS90k + uint64(msg.Duration90k)
	if len(track.Samples) == 1 {
		track.Start90k = msg.PTS90k
	}
	b.lastPTS[msg.PID] = msg.PTS90k
	b.havePTS[msg.PID] = true
	b.updateDuration(track.End90k)
	return nil
}

func (b *TrackBuilder) track(pid uint16, kind TrackKind) *Track {
	if tr, ok := b.byPID[pid]; ok {
		return tr
	}
	tr := &Track{
		ID:   fmt.Sprintf("%s-%04x", kind, pid),
		PID:  pid,
		Kind: kind,
	}
	b.byPID[pid] = tr
	b.program.Tracks = append(b.program.Tracks, tr)
	return tr
}

func (b *TrackBuilder) updateDuration(end uint64) {
	if end > b.program.Duration90k {
		b.program.Duration90k = end
	}
}

// Build finalizes the program and attaches shared transport observations.
func (b *TrackBuilder) Build(gaps []GapEvent, wraps int, inputPackets uint64) *NormalizedProgram {
	for _, tr := range b.program.Tracks {
		if len(tr.Samples) > 0 {
			tr.Start90k = tr.Samples[0].PTS90k
			last := tr.Samples[len(tr.Samples)-1]
			tr.End90k = last.PTS90k + last.Duration90k
		}
	}
	b.program.Gaps = append([]GapEvent(nil), gaps...)
	b.program.Wraps = wraps
	b.program.InputPackets = inputPackets
	return &b.program
}

// TrackByKind returns the first track of the requested kind.
func (p *NormalizedProgram) TrackByKind(kind TrackKind) *Track {
	for _, tr := range p.Tracks {
		if tr.Kind == kind {
			return tr
		}
	}
	return nil
}
