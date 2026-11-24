// Package fixture builds self-contained sample media in memory: a valid
// MP4/fMP4 with multiple timescales and cross-track timed metadata, plus the
// deliberately damaged variants the demo and tests exercise.
package fixture

import (
	"bytes"
)

// BuildTruncatedBox returns the valid classic fixture with the final samples
// of mdat cut off.  Damage.Offset is the first missing byte: the end of the
// input, because the declaration needs bytes the file no longer has.
func BuildTruncatedBox(opts Options) Payload {
	valid := BuildValid(opts)
	data := valid.Data
	cut := len(data) - 3
	out := append([]byte(nil), data[:cut]...)
	// Truncating mid-mdat only bounds the last sample's track, so also cut a
	// full 7 bytes to guarantee the last subtitle sample is short.  The
	// reported offset stays the end of the input: that is the first byte the
	// container needs.
	return Payload{
		Name: "truncated-box.mp4", Data: out,
		Damage: &Damage{Kind: "truncated-box", Offset: int64(len(out)), Detail: "mdat payload truncated"},
	}
}

// BuildRTP returns a length-framed RTP stream with a deliberately skipped
// sequence number.  Damage.Offset points at the first byte of the packet whose
// sequence number is wrong.
func BuildRTP() Payload {
	const ssrc = 0x11223344
	var out wbuf
	type pkt struct {
		seq  uint16
		ts   uint32
		pt   uint8
		data []byte
	}
	packets := []pkt{
		{100, 0, 96, []byte("rtp-0")},
		{101, 3000, 96, []byte("rtp-1")},
		{103, 6000, 96, []byte("rtp-2")}, // 102 missing
		{104, 9000, 96, []byte("rtp-3")},
	}
	var damage *Damage
	for _, p := range packets {
		raw := rtpPacket(p.seq, p.ts, ssrc, p.pt, p.data)
		if p.seq == 103 {
			damage = &Damage{Kind: "rtp-sequence", Offset: int64(out.b.Len()), Detail: "sequence 102 missing"}
		}
		out.u32(uint32(len(raw)))
		raw = append([]byte(nil), raw...)
		out.raw(raw)
	}
	return Payload{Name: "damaged-rtp.bin", Data: out.bytes(), Damage: damage}
}

func rtpPacket(seq uint16, ts, ssrc uint32, pt uint8, payload []byte) []byte {
	var b wbuf
	b.u8(0x80)
	b.u8(pt)
	b.u16(seq)
	b.u32(ts)
	b.u32(ssrc)
	b.raw(payload)
	return b.bytes()
}

// BuildMissingIndex returns a fragmented file with neither sidx nor mfra.
func BuildMissingIndex(opts Options) Payload {
	opts.Fragmented = true
	opts.DropIndex = true
	p := BuildValid(opts)
	p.Name = "missing-index.mp4"
	// The index is reported missing at the first moof, which the recovery
	// stage re-scans sequentially.
	scan := scanFirstMoof(p.Data)
	p.Damage = &Damage{Kind: "missing-index", Offset: scan, Detail: "fragmented file has no index"}
	return p
}

// BuildRecoverable returns a fragmented file with a missing index and a
// complete moof/mdat prefix followed by a cut tail, plus the byte ranges that
// are verified.
func BuildRecoverable(opts Options) (Payload, []Range) {
	opts.Fragmented = true
	opts.DropIndex = true
	valid := BuildValid(opts)
	data := valid.Data
	// Find the start of the last moof and cut inside its mdat, leaving the
	// earlier fragments fully intact.
	last := bytes.LastIndex(data, []byte("moof")) - 4
	if last <= 0 {
		last = len(data) / 2
	}
	cut := len(data) - 5
	if cut <= last {
		cut = last + 4
	}
	out := append([]byte(nil), data[:cut]...)
	ranges := []Range{{Start: 0, End: int64(last)}}
	return Payload{
		Name: "recoverable.mp4", Data: out,
		Damage: &Damage{Kind: "truncated-tail", Offset: int64(cut), Detail: "tail cut mid-fragment"},
	}, ranges
}

func scanFirstMoof(data []byte) int64 {
	idx := bytes.Index(data, []byte("moof"))
	if idx < 4 {
		return 0
	}
	return int64(idx - 4)
}
