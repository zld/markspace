// Package fixture generates deterministic sample assets for the regression
// harness. Everything is derived from the numeric seed so a given seed always
// produces byte-identical inputs: healthy and damaged subtitles, MP4 files
// (including a corrupt length field), and RTP arrival streams containing
// duplicates, reordering, burst loss and continuously varying arrival gaps.
package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"
)

// Assets bundles every generated input for one run.
type Assets struct {
	Seed int64

	// Subtitles: Base is healthy; Modified changes one cue's payload only.
	SRTBase     []byte
	SRTModified []byte
	VTTBase     []byte

	// MP4: Healthy scans clean; Corrupt has one stsz length field damaged.
	MP4Healthy  []byte
	MP4Corrupt  []byte
	CorruptOffset int64 // exact byte offset of the damaged size field

	// RTP arrival stream with injected impairments.
	RTPPackets []*rtp.Packet
	// ArrivalGaps is the per-packet inter-arrival spacing (in abstract units);
	// it varies continuously rather than holding a fixed pacing value.
	ArrivalGaps []int
	// Impairment counters the harness asserts against.
	RTPDuplicates int
	RTPReordered  int
	RTPBurstLost  int
}

// Generate builds the full deterministic asset set from a seed.
func Generate(seed int64) *Assets {
	a := &Assets{Seed: seed}

	a.SRTBase = buildSRT(seed, false)
	a.SRTModified = buildSRT(seed, true)
	a.VTTBase = buildVTT(seed)

	a.MP4Healthy = buildMP4(seed, false)
	healthy := a.MP4Healthy
	corrupt := append([]byte(nil), healthy...)
	idx := bytes.Index(corrupt, []byte("stsz"))
	a.CorruptOffset = int64(idx - 4)
	// Damage the stsz 32-bit size field so it extends beyond its parent box.
	binary.BigEndian.PutUint32(corrupt[a.CorruptOffset:], 0xFFFFFFFF)
	a.MP4Corrupt = corrupt

	var gaps []int
	a.RTPPackets, gaps, a.RTPDuplicates, a.RTPReordered, a.RTPBurstLost = buildRTP()
	a.ArrivalGaps = gaps
	return a
}

// buildSRT renders a small deterministic SRT document. When modifyOne is true
// the payload of a single cue changes while all timing and framing are kept.
func buildSRT(seed int64, modifyOne bool) []byte {
	var b bytes.Buffer
	cues := []struct{ start, end, text string }{
		{"00:00:01,000", "00:00:02,500", "alpha cue"},
		{"00:00:03,000", "00:00:04,000", "beta cue"},
		{"00:00:05,000", "00:00:06,250", "gamma cue"},
	}
	for i, c := range cues {
		text := c.text
		if modifyOne && i == 1 {
			text = "beta cue changed"
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\nseed-%d %s\n\n",
			i+1, c.start, c.end, seed, text)
	}
	return b.Bytes()
}

// buildVTT renders the WebVTT counterpart of the SRT content.
func buildVTT(seed int64) []byte {
	var b bytes.Buffer
	b.WriteString("WEBVTT\n\n")
	cues := []struct{ start, end, text string }{
		{"00:00:01.000", "00:00:02.500", "alpha cue"},
		{"00:00:03.000", "00:00:04.000", "beta cue"},
	}
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\nseed-%d %s\n\n",
			i+1, c.start, c.end, seed, c.text)
	}
	return b.Bytes()
}

// buildMP4 assembles a minimal two-track ftyp+moov+mdat file using raw boxes,
// mirroring the layout the mp4 scanner understands.
func buildMP4(seed int64, _ bool) []byte {
	ftyp := rawBox("ftyp", append([]byte("isom"), 0, 0, 0, 0))
	mvhd := rawBox("mvhd", mvhdPayload())
	trak1 := buildTrak(1, 1000, 40, []uint32{10, 10, 10, 10},
		[]uint32{10, 10, 10, 10})
	trak2 := buildTrak(2, 500, 20, []uint32{5, 5}, []uint32{5, 5})
	moov := rawBox("moov", bytes.Join([][]byte{mvhd, trak1, trak2}, nil))
	// mdat payload size matches the declared per-sample sizes in aggregate.
	mdat := rawBox("mdat", bytes.Repeat([]byte{byte(seed)}, 50))
	return bytes.Join([][]byte{ftyp, moov, mdat}, nil)
}

func mvhdPayload() []byte {
	p := make([]byte, 96)
	binary.BigEndian.PutUint32(p[4:8], 0)   // creation
	binary.BigEndian.PutUint32(p[8:12], 0)  // modification
	binary.BigEndian.PutUint32(p[12:16], 1000) // timescale
	binary.BigEndian.PutUint32(p[16:20], 40)   // duration
	return p
}

func buildTrak(id, timescale, duration uint32, sizes, deltas []uint32) []byte {
	mdhd := rawBox("mdhd", mdhdPayload(timescale, duration))
	tkhd := rawBox("tkhd", tkhdPayload(id, duration))

	stsz := rawBox("stsz", stszPayload(sizes))
	stco := rawBox("stco", concat(fullBox(), u32b(1), u32b(0)))
	stsc := rawBox("stsc", stscPayload(uint32(len(sizes))))
	stts := rawBox("stts", sttsPayload(deltas))
	stbl := rawBox("stbl", bytes.Join([][]byte{stsz, stco, stsc, stts}, nil))
	minf := rawBox("minf", stbl)
	mdia := rawBox("mdia", bytes.Join([][]byte{mdhd, minf}, nil))
	return rawBox("trak", bytes.Join([][]byte{tkhd, mdia}, nil))
}

func mdhdPayload(timescale, duration uint32) []byte {
	p := make([]byte, 28)
	binary.BigEndian.PutUint32(p[4:8], 0)
	binary.BigEndian.PutUint32(p[8:12], 0)
	binary.BigEndian.PutUint32(p[12:16], timescale)
	binary.BigEndian.PutUint32(p[16:20], duration)
	return p
}

func tkhdPayload(id, duration uint32) []byte {
	p := make([]byte, 84)
	binary.BigEndian.PutUint32(p[12:16], id)
	binary.BigEndian.PutUint32(p[20:24], duration)
	return p
}

func stszPayload(sizes []uint32) []byte {
	p := concat(fullBox(), u32b(0), u32b(uint32(len(sizes))))
	for _, s := range sizes {
		p = append(p, u32b(s)...)
	}
	return p
}

func stscPayload(samplesPerChunk uint32) []byte {
	return concat(fullBox(), u32b(1), u32b(1), u32b(samplesPerChunk), u32b(1))
}

func sttsPayload(deltas []uint32) []byte {
	p := concat(fullBox(), u32b(uint32(len(deltas))))
	for _, d := range deltas {
		p = append(p, u32b(1)...)
		p = append(p, u32b(d)...)
	}
	return p
}

// buildRTP constructs an arrival-order packet stream and returns the injected
// impairment counts plus a continuously varying inter-arrival gap schedule.
func buildRTP() ([]*rtp.Packet, []int, int, int, int) {
	const ssrc uint32 = 0x5EED
	const baseSeq = 100
	const total = 40
	var ordered []*rtp.Packet
	for i := 0; i < total; i++ {
		seq := uint16(baseSeq + i)
		ordered = append(ordered, mkPacket(ssrc, seq, uint32(i)*160))
	}

	dups := 0
	reord := 0
	burst := 4 // contiguous burst loss: seqs 120..123

	arrival := make([]*rtp.Packet, 0, total+2)
	// Burst-lost indices (relative): 20..23.
	lost := map[int]bool{20: true, 21: true, 22: true, 23: true}
	for i, p := range ordered {
		if lost[i] {
			continue
		}
		arrival = append(arrival, p)
		// Bounded reordering via three adjacent swaps. Each swap makes a
		// higher sequence number observed before its predecessor, which the
		// recoverer counts as exactly one reordering.
		if i == 10 && len(arrival) >= 2 {
			arrival[len(arrival)-1], arrival[len(arrival)-2] =
				arrival[len(arrival)-2], arrival[len(arrival)-1]
			reord++
		}
	}
	// Two more adjacent swaps in the tail give three reorder events total.
	for _, at := range []int{len(arrival) - 4, len(arrival) - 8} {
		if at >= 0 && at+1 < len(arrival) {
			arrival[at], arrival[at+1] = arrival[at+1], arrival[at]
			reord++
		}
	}
	// Insert two exact duplicates of distinct surviving packets. Duplicates
	// do not change ordering or the packet multiset.
	for _, dupIdx := range []int{5, 15} {
		arrival = append(arrival, clonePacket(ordered[dupIdx]))
		dups++
	}
	// Continuously varying inter-arrival gaps: an alternating sweep guarantees
	// no two adjacent gaps are equal, perturbing timing without reordering.
	gaps := make([]int, len(arrival))
	for i := range arrival {
		// Values oscillate over 4..10, changing at every position.
		gaps[i] = 4 + (i*3)%7
	}
	return arrival, gaps, dups, reord, burst
}

func mkPacket(ssrc uint32, seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96,
			SequenceNumber: seq, Timestamp: ts, SSRC: ssrc,
		},
		Payload: []byte{byte(seq >> 8), byte(seq), 0xAA},
	}
}

func clonePacket(p *rtp.Packet) *rtp.Packet {
	cp := *p
	cp.Payload = append([]byte(nil), p.Payload...)
	return &cp
}

func rawBox(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox() []byte { return []byte{0, 0, 0, 0} }

func u32b(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func concat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
