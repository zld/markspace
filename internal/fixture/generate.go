// Package fixture generates deterministic frame streams, RTP packet streams
// and fault samples entirely in memory. Nothing here reads external files.
package fixture

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"mediacore/ingest"
	"mediacore/media"
)

// Defaults for the demonstration material.
const (
	DefaultFrames        = 1200
	DefaultWidth         = 64
	DefaultHeight        = 64
	DefaultFrameMS int64 = 40
	// MetaSize is the target metadata length; the encoder verifies it is
	// achieved exactly so record offsets are predictable.
	MetaSize = 158
	// PacketPayload is chosen so the ingest bandwidth estimate is 900 kbps at
	// a 40 ms frame cadence: 4500 bytes * 8 / 0.04 s.
	PacketPayload = 4500
)

// Options configure fixture generation.
type Options struct {
	Frames    int
	Width     uint16
	Height    uint16
	FrameMS   int64
	Timescale uint32
	// WrapTimestamps starts the RTP timestamp near the 32-bit boundary so the
	// stream crosses a wraparound.
	WrapTimestamps bool
}

// DefaultOptions returns the standard 1200-frame, 64x64, 40 ms sample.
func DefaultOptions() Options {
	return Options{
		Frames:    DefaultFrames,
		Width:     DefaultWidth,
		Height:    DefaultHeight,
		FrameMS:   DefaultFrameMS,
		Timescale: media.DefaultTimescale,
	}
}

func (o Options) normalized() Options {
	d := DefaultOptions()
	if o.Frames <= 0 {
		o.Frames = d.Frames
	}
	if o.Width == 0 {
		o.Width = d.Width
	}
	if o.Height == 0 {
		o.Height = d.Height
	}
	if o.FrameMS <= 0 {
		o.FrameMS = d.FrameMS
	}
	if o.Timescale == 0 {
		o.Timescale = d.Timescale
	}
	return o
}

// Plan describes the intended content of a generated stream.
type Plan struct {
	Frames         int
	Width          uint16
	Height         uint16
	FrameMS        int64
	Timescale      uint32
	QuietStart     int
	QuietCount     int
	DuplicatePairs [][2]int
}

// DefaultPlan returns the standard content plan: one 2.4 s quiet interval and
// three duplicate pairs.
func DefaultPlan() Plan {
	return Plan{
		Frames:         DefaultFrames,
		Width:          DefaultWidth,
		Height:         DefaultHeight,
		FrameMS:        DefaultFrameMS,
		Timescale:      media.DefaultTimescale,
		QuietStart:     300,
		QuietCount:     60, // 60 * 40 ms = 2400 ms
		DuplicatePairs: [][2]int{{500, 700}, {501, 701}, {502, 702}},
	}
}

// contentID maps a frame index to its content identity. Paired frames share an
// identity so their payloads are byte-identical while timestamps differ.
func (p Plan) contentID(i int) int {
	for _, pair := range p.DuplicatePairs {
		if i == pair[1] {
			return pair[0]
		}
	}
	return i
}

// volumeFor returns the dBFS summary for a frame: quiet inside the plan's
// silent window, otherwise a steady -12.5 dBFS.
func (p Plan) volumeFor(i int) float64 {
	if i >= p.QuietStart && i < p.QuietStart+p.QuietCount {
		return -60
	}
	return -12.5
}

// payload returns deterministic pixel bytes for a content identity.
func payload(contentID int, size int) []byte {
	seed := sha256.Sum256([]byte(fmt.Sprintf("mediacore-frame-%d", contentID)))
	out := make([]byte, size)
	for off := 0; off < size; off += len(seed) {
		copy(out[off:], seed[:])
		seed = sha256.Sum256(seed[:])
	}
	return out
}

// metaBytes builds exactly MetaSize bytes of metadata for a frame.
func metaBytes(volume float64, contentID int) []byte {
	base := struct {
		VolumeDBFS float64           `json:"volume_dbfs"`
		Extra      map[string]string `json:"extra"`
	}{VolumeDBFS: volume, Extra: map[string]string{"pad": ""}}
	raw, _ := json.Marshal(base)
	pad := MetaSize - len(raw)
	if pad < 0 {
		pad = 0
	}
	base.Extra["pad"] = strings.Repeat("x", pad)
	raw, _ = json.Marshal(base)
	// If the pad made the document longer than MetaSize, trim the pad until it
	// fits. JSON escaping does not apply to 'x', so this terminates quickly.
	for len(raw) > MetaSize && pad > 0 {
		pad--
		base.Extra["pad"] = strings.Repeat("x", pad)
		raw, _ = json.Marshal(base)
	}
	return raw
}

// BuildFrames generates the planned frame records.
func (p Plan) BuildFrames(opts Options) ([]*media.Frame, error) {
	opts = opts.normalized()
	p.Frames = opts.Frames
	p.Width = opts.Width
	p.Height = opts.Height
	p.FrameMS = opts.FrameMS
	p.Timescale = opts.Timescale
	step := uint32(opts.FrameMS * int64(opts.Timescale) / 1000)
	var startTs uint32
	if opts.WrapTimestamps {
		// Start ~18 frames before the 32-bit boundary.
		startTs = 0xFFFFFFFF - step*18 + 1
	}
	size := int(opts.Width) * int(opts.Height)
	frames := make([]*media.Frame, opts.Frames)
	for i := 0; i < opts.Frames; i++ {
		f := &media.Frame{
			// Sequence numbers are the RTP 16-bit space, so ingest sees the
			// same wraparound behaviour a real stream would.
			Seq:           uint32(uint16(i)),
			RTPTimestamp:  startTs + uint32(int64(i)*int64(step)),
			CaptureTimeMS: int64(i) * opts.FrameMS,
			Width:         opts.Width,
			Height:        opts.Height,
			DurationTicks: step,
			StreamID:      1,
			Payload:       payload(p.contentID(i), size),
			MetaRaw:       metaBytes(p.volumeFor(i), p.contentID(i)),
		}
		frames[i] = f
	}
	return frames, nil
}

// EncodeFrames serializes frames and returns the bytes plus the exact total.
func EncodeFrames(frames []*media.Frame) ([]byte, error) {
	return media.EncodeStream(frames)
}

// Packets builds one RTP packet per frame with the configured payload size.
func Packets(frames []*media.Frame, payloadSize int) ([]byte, error) {
	if payloadSize <= 0 {
		payloadSize = PacketPayload
	}
	var out []byte
	for _, f := range frames {
		p := &ingest.Packet{
			StreamID:  f.StreamID,
			Seq:       uint16(f.Seq),
			Timestamp: f.RTPTimestamp,
			Marker:    true,
			SSRC:      0x11223344,
			Payload:   make([]byte, payloadSize),
		}
		for i := range p.Payload {
			p.Payload[i] = byte(f.Seq + uint32(i))
		}
		b, err := ingest.EncodePacket(p)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// ExpectedSummary documents the clean demo's intended output.
func (p Plan) ExpectedSummary() string {
	return fmt.Sprintf("frames=%d segments=%d silence_ms=%d duplicate_groups=%d",
		p.Frames, p.Frames/100, (p.QuietCount * int(p.FrameMS)), len(p.DuplicatePairs))
}
