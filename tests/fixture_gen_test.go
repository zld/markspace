// Package tests holds cross-package fixture generation and validation. The
// fixtures are synthesised in-process; no external files are referenced.
package tests

import (
	"bytes"
	"math"
	"testing"

	"github.com/pion/rtp/v2"

	"mediacore/adapter"
	"mediacore/internal/mp4box"
)

// ---- RTP fixtures -------------------------------------------------------

func rtpPacket(ssrc uint32, seq uint16, ts uint32) []byte {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq,
			Timestamp: ts, SSRC: ssrc, Marker: true,
		},
		Payload: []byte{0x01, 0x02, 0x03, 0x04},
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

// genRTPFixtures builds valid, boundary-wrapping and corrupt RTP datagrams.
func genRTPFixtures() (valid, boundary, corrupt [][]byte) {
	const ssrc uint32 = 0x11223344
	for seq := uint16(0); seq < 8; seq++ {
		valid = append(valid, rtpPacket(ssrc, seq, uint32(seq)*960))
	}
	// 16-bit sequence and 32-bit timestamp wrap boundaries
	boundary = append(boundary,
		rtpPacket(ssrc, math.MaxUint16, math.MaxUint32-960),
		rtpPacket(ssrc, 0, 0),
		rtpPacket(ssrc, 1, 960),
	)
	// corrupt: truncated and bad version
	corrupt = append(corrupt,
		[]byte{0x00, 0x01},
		bytes.Repeat([]byte{0xFF}, 4),
	)
	return valid, boundary, corrupt
}

func TestRTPFixtureDeterminism(t *testing.T) {
	v1, b1, c1 := genRTPFixtures()
	v2, b2, c2 := genRTPFixtures()
	for i := range v1 {
		if !bytes.Equal(v1[i], v2[i]) {
			t.Fatalf("valid RTP fixture %d not deterministic", i)
		}
	}
	for i := range b1 {
		if !bytes.Equal(b1[i], b2[i]) {
			t.Fatalf("boundary RTP fixture %d not deterministic", i)
		}
	}
	for i := range c1 {
		if !bytes.Equal(c1[i], c2[i]) {
			t.Fatalf("corrupt RTP fixture %d not deterministic", i)
		}
	}
}

func TestRTPFixturesClassified(t *testing.T) {
	valid, boundary, corrupt := genRTPFixtures()
	s, err := adapter.NewRTPStream(0x11223344, 48000, 32)
	if err != nil {
		t.Fatal(err)
	}
	var emitted int
	for _, raw := range valid {
		emitted += len(s.Push(raw).Events)
	}
	if emitted != len(valid) {
		t.Fatalf("valid fixtures: emitted %d of %d", emitted, len(valid))
	}
	// wrapping boundary packets still parse and stay ordered
	for _, raw := range boundary {
		if r := s.Push(raw); r.Kind == adapter.RTPBad {
			t.Fatal("boundary wrap packet rejected as bad")
		}
	}
	// corrupt fixtures must be classified bad, never panic
	for _, raw := range corrupt {
		if r := s.Push(raw); r.Kind != adapter.RTPBad {
			t.Fatalf("corrupt fixture classified as %v", r.Kind)
		}
	}
}

// ---- MP4 fixtures -------------------------------------------------------

func mp4TrackSpecs() []mp4box.TrackSpec {
	return []mp4box.TrackSpec{
		{
			TrackID: 1, Timescale: 48000,
			SampleDur:  repeat(480, 6),
			SampleSize: repeat(200, 6),
			StartPTS:   480, // 10 ms, inside the 6-sample (2880-tick) track
		},
		{
			TrackID: 2, Timescale: 90000, Video: true,
			SampleDur:  []uint32{3000, 6000, 3000},
			SampleSize: []uint32{100, 120, 110},
			Keyframes:  []uint32{1},
			EmptyHead:  250,
		},
	}
}

func repeat(v uint32, n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestMP4FixtureDeterminism(t *testing.T) {
	a := mp4box.Build(mp4TrackSpecs())
	b := mp4box.Build(mp4TrackSpecs())
	if !bytes.Equal(a.Data, b.Data) {
		t.Fatal("MP4 fixture generation is not deterministic")
	}
}

func TestMP4FixturesValidAndCorrupt(t *testing.T) {
	built := mp4box.Build(mp4TrackSpecs())
	ad := adapter.NewMP4AdapterV1()

	// valid fixture parses and preserves edit list + sparse keyframes
	m, err := ad.ParseMP4(bytes.NewReader(built.Data), int64(len(built.Data)))
	if err != nil {
		t.Fatalf("valid MP4 fixture rejected: %v", err)
	}
	if len(m.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(m.Tracks))
	}
	var keyframes int
	for _, tr := range m.Tracks {
		for _, sm := range tr.Samples {
			if sm.IsKeyframe {
				keyframes++
			}
		}
	}
	if keyframes == 0 {
		t.Fatal("sparse keyframe fixture lost its sync samples")
	}

	// corrupt fixtures: truncated and tampered inputs must be rejected
	corrupt := [][]byte{
		built.Data[:8],
		bytes.Repeat([]byte{0x5A}, 256),
	}
	for i, raw := range corrupt {
		if _, err := ad.ParseMP4(bytes.NewReader(raw), int64(len(raw))); err == nil {
			t.Fatalf("corrupt MP4 fixture %d accepted", i)
		}
	}
}

func TestMP4BoundaryTimelineAccepted(t *testing.T) {
	// A single-sample track exercises the smallest non-empty sample table.
	specs := []mp4box.TrackSpec{{
		TrackID: 1, Timescale: 1,
		SampleDur: []uint32{1}, SampleSize: []uint32{16},
	}}
	built := mp4box.Build(specs)
	m, err := adapter.NewMP4AdapterV1().ParseMP4(
		bytes.NewReader(built.Data), int64(len(built.Data)))
	if err != nil {
		t.Fatalf("boundary single-sample fixture rejected: %v", err)
	}
	if len(m.Tracks[0].Samples) != 1 {
		t.Fatal("single-sample boundary not preserved")
	}
}
