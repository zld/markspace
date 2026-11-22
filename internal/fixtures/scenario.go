// Package fixtures generates the project's own media scenario, packetizes it
// into restricted RTP payloads and simulates edge-network faults. Nothing is
// read from disk or the network: every byte is derived from the seed.
package fixtures

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"

	"mediapipe/internal/clock"
	"mediapipe/internal/meta"
	"mediapipe/internal/mp4"
)

// Scenario shape. Bounds keep the demo and tests small and fast.
const (
	Segments       = 6
	VideoFrames    = 4
	AudioSamples   = 8
	VideoTimescale = 90000
	AudioTimescale = 48000
	VideoFrameDur  = 3000
	AudioSampleDur = 800
	AudioOffset    = 1920 // 40 ms at 48 kHz, applied to every segment
	MaxFragment    = 48
)

// Group kinds.
const (
	KindVideo = "video"
	KindAudio = "audio"
	KindMeta  = "meta"
)

// Group is one decodable unit (sample or metadata record) before fragmentation.
type Group struct {
	Index        int
	Segment      uint32
	TrackID      uint32
	Frame        uint32
	Kind         string
	UnifiedStart int64
	UnifiedEnd   int64
	Keyframe     bool
	Data         []byte
	Digest       [32]byte
	FragCount    int
	// PacketSeqs holds the absolute sequence number of each fragment.
	PacketSeqs []uint16
}

// RawPacket is one packet in sequence order, before perturbation.
type RawPacket struct {
	Seq        uint16
	Segment    uint32
	GroupIndex int
	TrackID    uint32
	Frame      uint32
	Flags      uint8
	MediaTicks uint64
	FragIndex  uint16
	FragCount  uint16
	Data       []byte
}

// Scenario is the deterministic generated world.
type Scenario struct {
	Seed        int64
	Tracks      []mp4.TrackSpec
	Segments    []mp4.SegmentSpec
	MP4         []byte
	Groups      []*Group
	Packets     []RawPacket
	Records     []meta.Record
	Fingerprint []byte
}

// TrackTimescale returns the RTP clock rate of a generated track.
func TrackTimescale(trackID uint32) uint32 {
	switch trackID {
	case 1:
		return VideoTimescale
	case 2:
		return AudioTimescale
	default:
		return clock.UnifiedTimescale
	}
}

func contentBytes(seed int64, label string, n int) []byte {
	out := make([]byte, 0, n)
	var ctr uint32
	for len(out) < n {
		h := sha256.New()
		fmt.Fprintf(h, "%d|%s|%d", seed, label, ctr)
		sum := h.Sum(nil)
		out = append(out, sum...)
		ctr++
	}
	return out[:n]
}

// Build constructs the scenario. Identical seeds yield identical bytes.
func Build(seed int64) (*Scenario, error) {
	if seed < 0 {
		return nil, fmt.Errorf("fixtures: seed must be non-negative")
	}
	sc := &Scenario{Seed: seed}
	sc.Tracks = []mp4.TrackSpec{
		{ID: 1, Handler: "vide", Timescale: VideoTimescale, Width: 1280, Height: 720,
			Color: &mp4.Colr{Primaries: 1, Transfer: 1, Matrix: 1}},
		{ID: 2, Handler: "soun", Timescale: AudioTimescale, Channels: 2, SampleSize: 16, SampleRate: AudioTimescale},
	}
	for seg := 0; seg < Segments; seg++ {
		segStart := uint64(seg) * VideoFrameDur * VideoFrames
		audioBase := uint64(seg)*6400 + AudioOffset
		spec := mp4.SegmentSpec{Sequence: uint32(seg + 1), DTS: map[uint32]uint64{1: segStart, 2: audioBase}}
		for f := 0; f < VideoFrames; f++ {
			size := 96 + int(contentBytes(seed, fmt.Sprintf("vsize/%d/%d", seg, f), 1)[0])%80
			payload := contentBytes(seed, fmt.Sprintf("video/%d/%d", seg, f), size)
			sync := f == 0
			spec.Samples = append(spec.Samples, mp4.SampleSpec{TrackID: 1, Duration: VideoFrameDur, Sync: sync, Payload: payload})
			unified := int64(segStart+uint64(f)*VideoFrameDur) * int64(clock.UnifiedTimescale) / int64(VideoTimescale)
			g := &Group{
				Segment: uint32(seg + 1), TrackID: 1, Frame: uint32(seg*VideoFrames + f), Kind: KindVideo,
				UnifiedStart: unified, UnifiedEnd: unified + VideoFrameDur*int64(clock.UnifiedTimescale)/int64(VideoTimescale),
				Keyframe: sync, Data: payload, Digest: sha256.Sum256(payload),
			}
			sc.Groups = append(sc.Groups, g)
		}
		for a := 0; a < AudioSamples; a++ {
			size := 48 + int(contentBytes(seed, fmt.Sprintf("asize/%d/%d", seg, a), 1)[0])%48
			payload := contentBytes(seed, fmt.Sprintf("audio/%d/%d", seg, a), size)
			spec.Samples = append(spec.Samples, mp4.SampleSpec{TrackID: 2, Duration: AudioSampleDur, Sync: true, Payload: payload})
			dts := int64(audioBase + uint64(a)*AudioSampleDur)
			unified := dts * int64(clock.UnifiedTimescale) / int64(AudioTimescale)
			g := &Group{
				Segment: uint32(seg + 1), TrackID: 2, Frame: uint32(seg*AudioSamples + a), Kind: KindAudio,
				UnifiedStart: unified, UnifiedEnd: unified + AudioSampleDur*int64(clock.UnifiedTimescale)/int64(AudioTimescale),
				Keyframe: true, Data: payload, Digest: sha256.Sum256(payload),
			}
			sc.Groups = append(sc.Groups, g)
		}
		metaStart := (segStart + 89) / 90 * 90
		spec.Emsg = []meta.Emsg{{SchemeIDURI: "urn:mediapipe:chapter", Value: fmt.Sprintf("seg-%d", seg+1),
			Timescale: clock.UnifiedTimescale, Presentation: metaStart, EventDuration: VideoFrameDur * VideoFrames,
			ID: uint32(seg + 1), Message: []byte(fmt.Sprintf("segment %d", seg+1))}}
		recs, err := segmentRecords(seed, seg, segStart)
		if err != nil {
			return nil, err
		}
		for i, r := range recs {
			_ = i
			sc.Records = append(sc.Records, r)
			unified := recUnified(r)
			dur := recDurationUnified(r)
			g := &Group{Segment: uint32(seg + 1), TrackID: 3, Frame: uint32(len(sc.Records) - 1), Kind: KindMeta,
				UnifiedStart: unified, UnifiedEnd: unified + dur, Data: r.Canonical(), Digest: sha256.Sum256(r.Canonical())}
			sc.Groups = append(sc.Groups, g)
		}
		sc.Segments = append(sc.Segments, spec)
	}
	// Fragment each group deterministically.
	var packets []RawPacket
	seq := uint16(0xFF00) // chosen so the run crosses the 16-bit wrap
	for gi, g := range sc.Groups {
		g.Index = gi
		g.FragCount = (len(g.Data) + MaxFragment - 1) / MaxFragment
		if g.FragCount == 0 || g.FragCount > 64 {
			return nil, fmt.Errorf("fixtures: group %d fragment count %d", gi, g.FragCount)
		}
		flags := uint8(0)
		switch g.Kind {
		case KindVideo:
			flags = 1 // FlagFragment
			if g.Keyframe {
				flags |= 1
			}
		case KindAudio:
			flags = 2 // FlagAudio
		default:
			flags = 4 // FlagMetadata
		}
		var mediaTicks uint64
		switch g.TrackID {
		case 1:
			mediaTicks = uint64(int64(g.UnifiedStart) * int64(VideoTimescale) / int64(clock.UnifiedTimescale))
		case 2:
			mediaTicks = uint64(int64(g.UnifiedStart) * int64(AudioTimescale) / int64(clock.UnifiedTimescale))
		default:
			mediaTicks = uint64(g.UnifiedStart)
		}
		for fi := 0; fi < g.FragCount; fi++ {
			g.PacketSeqs = append(g.PacketSeqs, seq)
			start := fi * MaxFragment
			end := start + MaxFragment
			if end > len(g.Data) {
				end = len(g.Data)
			}
			packets = append(packets, RawPacket{
				Seq: seq, Segment: g.Segment, GroupIndex: gi, TrackID: g.TrackID, Frame: g.Frame, Flags: flags,
				MediaTicks: mediaTicks, FragIndex: uint16(fi), FragCount: uint16(g.FragCount),
				Data: g.Data[start:end],
			})
			seq++
		}
	}
	sc.Packets = packets
	file, err := mp4.BuildFile(sc.Tracks, sc.Segments)
	if err != nil {
		return nil, err
	}
	sc.MP4 = file
	fp := sha256.New()
	fp.Write([]byte("mediapipe-fixtures/1"))
	_ = binary.Write(fp, binary.BigEndian, seed)
	for _, g := range sc.Groups {
		fp.Write(g.Digest[:])
	}
	sc.Fingerprint = fp.Sum(nil)
	return sc, nil
}

func segmentRecords(seed int64, seg int, segStart uint64) ([]meta.Record, error) {
	// The metadata track runs at 1 kHz while the segment boundary is derived
	// from 90 kHz video ticks. Round up so a segment never starts before the
	// previous one ends; truncation here would make metadata time regress.
	startMS := (segStart + 89) / 90
	endMS := (segStart + VideoFrameDur*VideoFrames + 89) / 90
	var out []meta.Record
	sub, err := meta.NewSubtitleRecord(3, meta.Cue{Seq: uint32(seg + 1), StartMS: startMS, EndMS: startMS + 100,
		Text: fmt.Sprintf("segment %d active", seg+1)})
	if err != nil {
		return nil, err
	}
	out = append(out, sub)
	chap, err := meta.NewChapterRecord(3, uint32(seg+1), startMS, endMS, fmt.Sprintf("Chapter %d", seg+1))
	if err != nil {
		return nil, err
	}
	out = append(out, chap)
	tag := meta.ID3Tag{Frames: []meta.ID3Frame{
		{ID: meta.ID3FrameTIT2, Encoding: 3, Text: fmt.Sprintf("edge segment %d", seg+1)},
		{ID: meta.ID3FrameTDRC, Encoding: 3, Text: fmt.Sprintf("2026-11-23T10:%02d:05.500Z", seg)},
		{ID: meta.ID3FrameTXXX, Encoding: 3, Owner: "mediapipe", Text: fmt.Sprintf("node=%d", seed%97)},
		{ID: meta.ID3FramePRIV, Owner: "mediapipe", Private: []byte{byte(seed), byte(seg), 0xde, 0xad}},
	}}
	id3, err := meta.NewID3Record(3, startMS, tag)
	if err != nil {
		return nil, err
	}
	out = append(out, id3)
	emsg, err := meta.NewEmsgRecord(3, meta.Emsg{SchemeIDURI: "urn:mediapipe:chapter", Value: fmt.Sprintf("seg-%d", seg+1),
		Timescale: clock.UnifiedTimescale, Presentation: startMS * 90, EventDuration: VideoFrameDur * VideoFrames,
		ID: uint32(seg + 1), Message: []byte(fmt.Sprintf("segment %d chapter", seg+1))})
	if err != nil {
		return nil, err
	}
	out = append(out, emsg)
	sei, err := meta.NewSEIRecord(3, startMS, meta.SEI{UUID: meta.SEIUUID,
		Payload: contentBytes(seed, fmt.Sprintf("sei/%d", seg), 24)})
	if err != nil {
		return nil, err
	}
	out = append(out, sei)
	return out, nil
}

func recUnified(r meta.Record) int64 {
	v, err := clock.Convert(int64(r.TimeTicks), r.Timescale, clock.UnifiedTimescale)
	if err != nil {
		return 0
	}
	return v
}

func recDurationUnified(r meta.Record) int64 {
	v, err := clock.Convert(int64(r.DurationTick), r.Timescale, clock.UnifiedTimescale)
	if err != nil {
		return 0
	}
	return v
}

// JitterArrival returns a deterministic arrival offset in unified ticks for a
// packet index. It is exported so tests can reason about ordering.
func JitterArrival(seed int64, index, jitterMS int) int64 {
	r := rand.New(rand.NewSource(seed*1000003 + int64(index)))
	span := int64(jitterMS) * 90
	base := int64(index) * 900
	if span == 0 {
		return base
	}
	return base + r.Int63n(2*span+1) - span
}
