// Package fixture generates all sample inputs and fault cases locally. No
// external files are referenced: containers, RTP sidecars and corrupted
// artifacts are synthesized from explicit numeric specifications.
package fixture

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pion/rtp"
	"mediapack/internal/probe"
	"mediapack/internal/timeline"
)

// TrackSpec declares one synthesized track.
type TrackSpec struct {
	ID        uint32
	Kind      probe.Kind
	ClockRate uint32
	Seconds   int
	// DriftPPM is the drift demonstrated against the external wall-clock
	// nominal reference; it does not perturb inter-track alignment.
	DriftPPM int64
	// WrapNear makes the 32-bit timestamp start close to 2^32 so a legal
	// wraparound occurs within the first seconds.
	WrapNear bool
	// VFR inserts irregular in-second frame gaps on video tracks while
	// keeping every whole-second keyframe exactly aligned.
	VFR bool
}

// DefaultSpecs builds n deterministic tracks: one video plus audio/data.
func DefaultSpecs(n int) []TrackSpec {
	specs := make([]TrackSpec, 0, n)
	for i := 0; i < n; i++ {
		s := TrackSpec{
			ID: uint32(i + 1), Seconds: 10, DriftPPM: 400 + int64(i)*50,
			WrapNear: true,
		}
		switch i % 3 {
		case 0:
			s.Kind, s.ClockRate, s.VFR = probe.KindVideo, 90000, true
		case 1:
			s.Kind, s.ClockRate = probe.KindAudio, 48000
		default:
			s.Kind, s.ClockRate = probe.KindData, 90000
		}
		specs = append(specs, s)
	}
	return specs
}

// Bundle is everything generated for one track.
type Bundle struct {
	Spec       TrackSpec
	Container  string // .mp4 path
	RTPSidecar string // length-prefixed RTP stream path
	NominalNS  int64  // external wall-clock span for drift estimation
}

// Generate writes containers and RTP sidecars for every spec into dir/inputs.
func Generate(dir string, specs []TrackSpec) ([]Bundle, error) {
	inDir := filepath.Join(dir, "inputs")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		return nil, err
	}
	out := make([]Bundle, 0, len(specs))
	for _, s := range specs {
		packets := buildPackets(s)
		stream, err := timeline.EncodeRTPStream(packets)
		if err != nil {
			return nil, err
		}
		ext := ".mp4"
		if s.Kind == probe.KindAudio {
			ext = ".mxf"
		}
		base := filepath.Join(inDir, trackName(s.ID))
		container := base + ext
		if err := writeContainer(container, s, len(packets)); err != nil {
			return nil, err
		}
		rtpPath := base + ".rtp"
		if err := os.WriteFile(rtpPath, stream, 0o600); err != nil {
			return nil, err
		}
		// Observed RTP span in ns is exact; nominal wall clock is shorter to
		// evidence a positive drift (clock runs fast).
		observedNS := int64(s.Seconds) * 1_000_000_000
		nominalNS := observedNS - (observedNS*s.DriftPPM)/1_000_000
		out = append(out, Bundle{
			Spec: s, Container: container, RTPSidecar: rtpPath, NominalNS: nominalNS,
		})
	}
	return out, nil
}

func trackName(id uint32) string {
	return "track-" + padID(id)
}

func padID(id uint32) string {
	return fmt.Sprintf("%03d", id)
}

// buildPackets synthesizes the RTP timestamp sequence. Whole-second keyframes
// land on exact rate ticks so every track shares those decodable instants.
func buildPackets(s TrackSpec) []rtp.Packet {
	var pkts []rtp.Packet
	var start uint32
	if s.WrapNear {
		start = uint32(0xFFFFFFFF) - uint32(s.ClockRate)*2 // wraps within ~2 s
	}
	seq := uint16(0)
	for sec := 0; sec < s.Seconds; sec++ {
		// Keyframe at the whole-second boundary: exact rate multiple.
		keyTS := start + uint32(int64(sec)*int64(s.ClockRate))
		pkts = append(pkts, markerPacket(s.ID, seq, keyTS))
		seq++
		if s.VFR && s.Kind == probe.KindVideo && sec < s.Seconds-1 {
			// Irregular in-second gaps summing to exactly one second of ticks.
			gaps := vfrGaps(s.ClockRate)
			cur := keyTS
			for _, g := range gaps {
				cur += uint32(g)
				pkts = append(pkts, markerPacket(s.ID, seq, cur))
				seq++
			}
		}
	}
	return pkts
}

// vfrGaps returns irregular sub-second tick gaps that sum to one full second.
func vfrGaps(rate uint32) []uint32 {
	// Four uneven gaps whose total is exactly `rate`.
	base := rate / 5
	gaps := []uint32{base, base * 2, base, 0}
	gaps[3] = rate - (gaps[0] + gaps[1] + gaps[2])
	return gaps
}

func markerPacket(track uint32, seq uint16, ts uint32) rtp.Packet {
	return rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts,
		SSRC: track, Marker: true,
	}, Payload: []byte{0x01}}
}

// ---- minimal container writers, readable by the probe package ----

func mp4Box(typ string, payload []byte, children ...[]byte) []byte {
	body := append([]byte{}, payload...)
	for _, c := range children {
		body = append(body, c...)
	}
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(body)))
	copy(hdr[4:], []byte(typ))
	return append(hdr, body...)
}

func writeContainer(path string, s TrackSpec, samples int) error {
	var data []byte
	if filepath.Ext(path) == ".mxf" {
		data = buildMXF(s, samples)
	} else {
		data = buildMP4(s, samples)
	}
	return os.WriteFile(path, data, 0o600)
}

func buildMP4(s TrackSpec, samples int) []byte {
	tk := make([]byte, 96)
	binary.BigEndian.PutUint32(tk[12:], s.ID)
	binary.BigEndian.PutUint32(tk[40:], 1<<16) // identity matrix -> 0 rotation
	binary.BigEndian.PutUint32(tk[56:], 1<<16)
	md := make([]byte, 32)
	binary.BigEndian.PutUint32(md[12:], s.ClockRate)
	binary.BigEndian.PutUint32(md[16:], s.ClockRate*uint32(s.Seconds))
	hd := make([]byte, 24)
	copy(hd[8:], []byte(handlerFor(s.Kind)))
	col := make([]byte, 12)
	copy(col[0:4], []byte("nclx"))
	binary.BigEndian.PutUint16(col[4:], 9)
	binary.BigEndian.PutUint16(col[6:], 16)
	binary.BigEndian.PutUint16(col[8:], 9)
	sz := make([]byte, 12)
	binary.BigEndian.PutUint32(sz[8:], uint32(samples))
	trak := mp4Box("trak", nil,
		mp4Box("tkhd", tk), mp4Box("mdhd", md),
		mp4Box("hdlr", hd), mp4Box("colr", col), mp4Box("stsz", sz))
	ftyp := mp4Box("ftyp", []byte("isom\x00\x00\x00\x00"))
	return append(ftyp, mp4Box("moov", nil, trak)...)
}

func handlerFor(k probe.Kind) string {
	switch k {
	case probe.KindVideo:
		return "vide"
	case probe.KindAudio:
		return "soun"
	default:
		return "meta"
	}
}
