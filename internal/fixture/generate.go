package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"

	"example.com/archiveverify/internal/cmaf"
)

// Workspace is a generated, self-contained input directory.
type Workspace struct {
	Dir          string
	ManifestPath string
	Manifest     *cmaf.Manifest
}

// Generate writes deterministic clean or fault-injected inputs into dir.
func Generate(dir, caseName string) (*Workspace, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	initPath := filepath.Join(dir, "init.mp4")
	if err := os.WriteFile(initPath, InitSegment(), 0o644); err != nil {
		return nil, err
	}

	m := &cmaf.Manifest{Case: caseName, InitPath: "init.mp4"}
	m.Events = append(m.Events, cmaf.Event{Kind: cmaf.EventInit, Path: "init.mp4", SourceID: "src-a"})

	seqs := []uint32{1, 2, 3, 4, 5, 6}
	if caseName == "faults" {
		seqs = []uint32{1, 2, 4, 5, 6, 7} // missing mfhd sequence 3
	}
	for i := 0; i < 6; i++ {
		vdt := uint32(i * 30_000)
		adt := uint32(i * 48_000)
		if caseName == "faults" && i == 3 {
			vdt = 57_000 // small same-source backward step from 60_000
		}
		data := Fragment(seqs[i], vdt, adt)
		name := filepath.ToSlash(filepath.Join("fragments", fragmentName(i)))
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return nil, err
		}
		m.Events = append(m.Events, cmaf.Event{
			Kind: cmaf.EventFragment, SourceID: "src-a", Path: name, Sequence: seqs[i],
		})
	}

	if caseName == "faults" {
		name := filepath.ToSlash(filepath.Join("fragments", "seg-truncated.m4s"))
		if err := os.WriteFile(filepath.Join(dir, name), TruncatedFragment(8), 0o644); err != nil {
			return nil, err
		}
		m.Events = append(m.Events, cmaf.Event{
			Kind: cmaf.EventFragment, SourceID: "src-a", Path: name, Sequence: 8,
		})
		m.Events = append(m.Events, cmaf.Event{
			Kind: cmaf.EventSlow, SourceID: "sink",
			Slow: &cmaf.SlowConsumerSpec{DelayMS: 2, Items: 4},
		})
	}

	m.Events = append(m.Events, cmaf.Event{
		Kind: cmaf.EventAnchor, SourceID: "src-a", TrackID: 1,
		Anchor: anchorFor(caseName),
	})
	m.RTPTrace = rtpTrace(caseName)

	for i := range m.Events {
		m.Events[i].Index = i
	}
	mp := filepath.Join(dir, "manifest.json")
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(mp, b, 0o644); err != nil {
		return nil, err
	}
	return &Workspace{Dir: dir, ManifestPath: mp, Manifest: m}, nil
}

func fragmentName(i int) string {
	return "seg-" + string(rune('0'+i)) + ".m4s"
}

func anchorFor(caseName string) *cmaf.AnchorSpec {
	expected := int64(2_000_200) // clean end-to-end offset: 0.2 ms
	if caseName == "faults" {
		expected = 2_002_000 // injected bounded sync error: 2.0 ms
	}
	return &cmaf.AnchorSpec{
		AnchorID: "video-at-2s", SourceID: "src-a", TrackID: 1,
		RTPTime: 180_000, RTPClockRate: 90_000,
		MediaRaw: 60_000, Timescale: 30_000, ExpectedUs: expected,
	}
}

func rtpTrace(caseName string) []cmaf.RTPPacketSpec {
	if caseName != "faults" {
		out := make([]cmaf.RTPPacketSpec, 0, 20)
		for seq := uint16(100); seq < 120; seq++ {
			out = append(out, cmaf.RTPPacketSpec{
				Sequence: seq, Timestamp: 180_000 + uint32(seq-100)*3000, SSRC: 0xabcdef,
				Marker: seq%10 == 9, Payload: []byte{byte(seq)},
			})
		}
		return out
	}

	lost := map[uint16]bool{103: true, 108: true, 113: true, 117: true}
	retrans := map[uint16]bool{103: true, 108: true, 113: true}
	order := []uint16{
		100, 101, 102, 101, // duplicate
		105, 104, // reorder
		106, 107,
		109, 110, 111, 112,
		114, 115, 116,
		118, 119,
	}
	out := make([]cmaf.RTPPacketSpec, 0, len(order)+6)
	for _, seq := range order {
		out = append(out, cmaf.RTPPacketSpec{
			Sequence: seq, Timestamp: 180_000 + uint32(seq-100)*3000, SSRC: 0xabcdef,
			Marker: seq%10 == 9, Payload: []byte{byte(seq)},
		})
	}
	// Three matching retransmissions for four lost originals.
	for seq := range retrans {
		out = append(out, cmaf.RTPPacketSpec{
			Sequence: seq, Timestamp: 180_000 + uint32(seq-100)*3000, SSRC: 0xabcdef,
			Payload: []byte{byte(seq)}, IsRetrans: true, OriginalSeq: seq,
		})
	}
	// A packet far outside the 16-sequence window.
	out = append(out, cmaf.RTPPacketSpec{Sequence: 70, Timestamp: 90_000, SSRC: 0xabcdef})
	_ = lost
	return out
}
