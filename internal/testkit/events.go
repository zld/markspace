package testkit

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pion/rtp"
)

// AnchorTick is the --rtp-anchor value used by every fixture: the first
// event packet maps to media time zero.
const AnchorTick = uint32(4294787296)

// eventLine is the on-disk JSONL schema consumed by events.Load.
type eventLine struct {
	Kind       string   `json:"kind,omitempty"`
	ID         string   `json:"id,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`
	DurationS  float64  `json:"duration_s,omitempty"`
	Packets    []string `json:"packets,omitempty"`
	Content    string   `json:"content,omitempty"`
}

// wrap32 adds ticks on the 32-bit RTP modulus.
func wrap32(base uint32, ticks int64) uint32 {
	return uint32((int64(base) + ticks) & 0xFFFFFFFF)
}

// rtpPacket builds one base64 RTP packet with the given timestamp and
// monotonic sequence number.
func rtpPacket(seq uint16, ts uint32, payload byte) (string, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96,
			SequenceNumber: seq, Timestamp: ts, SSRC: 0x5245504c,
		},
		Payload: []byte{payload},
	}
	raw, err := pkt.Marshal()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// writeJSONL streams lines through a buffered writer; the feed stays
// bounded because each record is a handful of packets.
func writeJSONL(path string, lines []eventLine) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	return w.Flush()
}

// cleanEvents builds the acceptance feed:
//   - a clock reference packet that crosses the 32-bit boundary (1 wrap)
//   - ev-a at t=2s duplicated with identical content
//   - ev-b at t=6s whose 4s aligned clip exceeds the 5s budget
//   - ev-c at t=8s below the confidence threshold
func cleanEvents() ([]eventLine, error) {
	// Anchor packet at the explicit anchor tick (media time zero).
	anchor, err := rtpPacket(1, AnchorTick, 0x01)
	if err != nil {
		return nil, err
	}
	// Reference packet past 2^32: shortest modular step is positive, the
	// unwrapper counts exactly one rollover.
	wrap, err := rtpPacket(2, 200000, 0x02)
	if err != nil {
		return nil, err
	}
	a1, err := rtpPacket(3, wrap32(AnchorTick, int64(2)*FixtureTimescale), 0x03) // t=2s
	if err != nil {
		return nil, err
	}
	a2, err := rtpPacket(4, wrap32(AnchorTick, int64(2)*FixtureTimescale), 0x04)
	if err != nil {
		return nil, err
	}
	// t=5s with a 3s event window: requested end is t=10s and the keyframe
	// back-off to t=5s makes the half-open aligned clip span 6s, exceeding
	// the 5s duration budget so the event is recorded as skip_budget.
	b, err := rtpPacket(5, wrap32(AnchorTick, int64(5)*FixtureTimescale), 0x05)
	if err != nil {
		return nil, err
	}
	c, err := rtpPacket(6, wrap32(AnchorTick, int64(8)*FixtureTimescale), 0x06) // t=8s
	if err != nil {
		return nil, err
	}
	return []eventLine{
		{Kind: "clock", Packets: []string{anchor}},
		{Kind: "clock", Packets: []string{wrap}},
		{ID: "ev-a", Confidence: 0.95, DurationS: 1, Content: "goal",
			Packets: []string{a1}},
		{ID: "ev-a", Confidence: 0.95, DurationS: 1, Content: "goal",
			Packets: []string{a2}},
		{ID: "ev-b", Confidence: 0.9, DurationS: 3, Content: "foul",
			Packets: []string{b}},
		{ID: "ev-c", Confidence: 0.4, DurationS: 1, Content: "crowd",
			Packets: []string{c}},
	}, nil
}

// gapEvents targets the event inside the gapped region so it must be
// rejected as untrusted; the later event stays valid and still parses.
func gapEvents() ([]eventLine, error) {
	bad, err := rtpPacket(10, wrap32(AnchorTick, int64(4)*FixtureTimescale), 0x10) // t=4s (gap zone)
	if err != nil {
		return nil, err
	}
	good, err := rtpPacket(11, wrap32(AnchorTick, int64(8)*FixtureTimescale), 0x11) // t=8s
	if err != nil {
		return nil, err
	}
	return []eventLine{
		{ID: "ev-gap", Confidence: 0.95, DurationS: 1, Content: "gap",
			Packets: []string{bad}},
		{ID: "ev-ok", Confidence: 0.95, DurationS: 1, Content: "ok",
			Packets: []string{good}},
	}, nil
}

// badRTPEvents contains one undecodable packet followed by a fully valid
// later event, proving bad packets are isolated rather than fatal.
func badRTPEvents() ([]eventLine, error) {
	good, err := rtpPacket(20, wrap32(AnchorTick, int64(3)*FixtureTimescale), 0x20)
	if err != nil {
		return nil, err
	}
	return []eventLine{
		// Not valid base64-of-RTP: the RTP unmarshal fails and is counted.
		{ID: "ev-bad", Confidence: 0.9, DurationS: 1, Content: "bad",
			Packets: []string{base64.StdEncoding.EncodeToString([]byte{0x80, 0x60})}},
		{ID: "ev-good", Confidence: 0.95, DurationS: 1, Content: "good",
			Packets: []string{good}},
	}, nil
}

func generateEventFiles(outDir string) error {
	clean, err := cleanEvents()
	if err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(outDir, "events.jsonl"), clean); err != nil {
		return err
	}
	gap, err := gapEvents()
	if err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(outDir, "events-gap.jsonl"), gap); err != nil {
		return err
	}
	bad, err := badRTPEvents()
	if err != nil {
		return err
	}
	return writeJSONL(filepath.Join(outDir, "events-bad-rtp.jsonl"), bad)
}
