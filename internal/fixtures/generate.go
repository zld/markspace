package fixtures

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pion/rtp"
)

// Kind enumerates the bundled acceptance fixtures.
type Kind string

const (
	KindNormal    Kind = "normal"
	KindTruncated Kind = "truncated"
	KindWrap      Kind = "wrap"
	KindTimebase  Kind = "timebase"
	KindDuplicate Kind = "duplicate"
)

// Inputs names the files that make up one fixture.
type Fixture struct {
	Kind       Kind
	Dir        string
	MediaPath  string
	EventsPath string
	// ClockRate is the RTP clock rate used for this fixture's events.
	ClockRate uint32
	// Expect describes the defect the fixture is meant to exercise.
	Expect string
}

// Timescales chosen so a video GOP boundary is not an integral number of
// timeline microseconds for the timebase fixture.
const (
	normalVideoTS = 90_000
	normalAudioTS = 48_000
	wrapClockRate = 90_000
	timebaseTS    = 30_000
)

// GenerateAll writes every fixture under root and returns their descriptors.
func GenerateAll(root string) ([]Fixture, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	var out []Fixture
	for _, k := range []Kind{KindNormal, KindTruncated, KindWrap, KindTimebase, KindDuplicate} {
		fx, err := Generate(root, k)
		if err != nil {
			return nil, err
		}
		out = append(out, fx)
	}
	return out, nil
}

// Generate writes one fixture.
func Generate(root string, kind Kind) (Fixture, error) {
	dir := filepath.Join(root, string(kind))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Fixture{}, err
	}
	fx := Fixture{Kind: kind, Dir: dir, ClockRate: wrapClockRate}
	switch kind {
	case KindNormal:
		fx.MediaPath = filepath.Join(dir, "replay.mp4")
		fx.EventsPath = filepath.Join(dir, "events.rtp")
		fx.Expect = "clean container and monotonic events"
		if err := writeFile(fx.MediaPath, normalMedia()); err != nil {
			return Fixture{}, err
		}
		events, err := rtpCapture(normalEvents(), fx.ClockRate)
		if err != nil {
			return Fixture{}, err
		}
		return fx, writeFile(fx.EventsPath, events)
	case KindTruncated:
		fx.MediaPath = filepath.Join(dir, "replay.mp4")
		fx.EventsPath = filepath.Join(dir, "events.rtp")
		fx.Expect = "mdat box declares more payload than the file carries"
		media := normalMedia()
		truncated, truncAt := truncateBox(media, "mdat", 37)
		if truncated == nil {
			return Fixture{}, fmt.Errorf("fixture layout: mdat box not found")
		}
		if err := writeFile(fx.MediaPath, truncated); err != nil {
			return Fixture{}, err
		}
		_ = truncAt
		events, err := rtpCapture(normalEvents(), fx.ClockRate)
		if err != nil {
			return Fixture{}, err
		}
		return fx, writeFile(fx.EventsPath, events)
	case KindWrap:
		fx.MediaPath = filepath.Join(dir, "replay.mp4")
		fx.EventsPath = filepath.Join(dir, "events.rtp")
		fx.ClockRate = wrapClockRate
		fx.Expect = "RTP timestamp wraps across 2^32 exactly once"
		if err := writeFile(fx.MediaPath, normalMedia()); err != nil {
			return Fixture{}, err
		}
		events, err := rtpCapture(wrapEvents(), fx.ClockRate)
		if err != nil {
			return Fixture{}, err
		}
		return fx, writeFile(fx.EventsPath, events)
	case KindTimebase:
		fx.MediaPath = filepath.Join(dir, "replay.mp4")
		fx.EventsPath = filepath.Join(dir, "events.rtp")
		fx.ClockRate = wrapClockRate
		fx.Expect = "30000 Hz video timebase with non-integral microsecond boundaries"
		if err := writeFile(fx.MediaPath, timebaseMedia()); err != nil {
			return Fixture{}, err
		}
		events, err := rtpCapture(timebaseEvents(), fx.ClockRate)
		if err != nil {
			return Fixture{}, err
		}
		return fx, writeFile(fx.EventsPath, events)
	case KindDuplicate:
		fx.MediaPath = filepath.Join(dir, "replay.mp4")
		fx.EventsPath = filepath.Join(dir, "events.rtp")
		fx.ClockRate = wrapClockRate
		fx.Expect = "duplicate event triggers cut the timeline twice at one tick"
		if err := writeFile(fx.MediaPath, normalMedia()); err != nil {
			return Fixture{}, err
		}
		events, err := rtpCapture(duplicateEvents(), fx.ClockRate)
		if err != nil {
			return Fixture{}, err
		}
		return fx, writeFile(fx.EventsPath, events)
	default:
		return Fixture{}, fmt.Errorf("unknown fixture kind %q", kind)
	}
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// eventSpec is a semantic cut request; convert it to an RTP timestamp.
type eventSpec struct {
	ssrc    uint32
	seq     uint16
	ts      uint32
	marker  bool
	payload string
}

// rtpCapture serializes event specs as newline-delimited hex packets.
func rtpCapture(specs []eventSpec, clockRate uint32) ([]byte, error) {
	var buf []byte
	for i, s := range specs {
		p := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         s.marker,
				PayloadType:    96,
				SequenceNumber: s.seq,
				Timestamp:      s.ts,
				SSRC:           s.ssrc,
			},
			Payload: []byte(s.payload),
		}
		wire, err := p.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshal event %d: %w", i, err)
		}
		buf = append(buf, []byte(hex.EncodeToString(wire))...)
		buf = append(buf, '\n')
	}
	return buf, nil
}
