// Package fixture generates the sample media used by tests and by the demo
// command. Everything is synthesised in code: no external assets, no network.
package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Box builders. All sizes stay below 2^32 so the 8-byte header form is used.

// Box returns a box with the given type and payload.
func Box(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

// FullBox returns a version/flags box.
func FullBox(typ string, version uint8, flags uint32, payload ...[]byte) []byte {
	hdr := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return Box(typ, append([][]byte{hdr}, payload...)...)
}

// U8/U16/U32/U64 encode big-endian integers.
func U8(v uint8) []byte   { return []byte{v} }
func U16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func U32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func U64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// Str4 returns exactly four bytes of type code.
func Str4(s string) []byte {
	b := make([]byte, 4)
	copy(b, s)
	return b
}

// Matrix is the identity display matrix used by tkhd.
func Matrix() []byte {
	m := []int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	var out []byte
	for _, v := range m {
		out = append(out, U32(uint32(v))...)
	}
	return out
}

// Ftyp builds a minimal ftyp box.
func Ftyp(major string) []byte {
	return Box("ftyp", Str4(major), U32(0x200), Str4("isom"), Str4("iso2"), Str4("mp41"))
}

// Mvhd builds an mvhd whose duration equals durationTicks.
func Mvhd(timescale, durationTicks uint32) []byte {
	payload := bytes.Join([][]byte{
		U32(0), U32(0), // creation, modification
		U32(timescale), U32(durationTicks),
		U32(0x00010000),     // rate 1.0
		U16(0x0100), U16(0), // volume 1.0, reserved
		U32(0), U32(0), // reserved
		Matrix(),
		make([]byte, 24), // pre_defined
		U32(2),           // next_track_ID
	}, nil)
	return FullBox("mvhd", 0, 0, payload)
}

// Tkhd builds a track header.
func Tkhd(trackID, durationTicks uint32, width, height uint16, volume uint16) []byte {
	w := uint32(width) << 16
	h := uint32(height) << 16
	payload := bytes.Join([][]byte{
		U32(0), U32(0),
		U32(trackID), U32(0),
		U32(durationTicks),
		make([]byte, 8), // reserved
		U16(0), U16(0),  // layer, alternate_group
		U16(volume), U16(0),
		Matrix(),
		U32(w), U32(h),
	}, nil)
	return FullBox("tkhd", 0, 0x000007, payload)
}

// Mdhd builds a media header.
func Mdhd(timescale, durationTicks uint32, lang string) []byte {
	var langCode uint16
	if len(lang) == 3 {
		langCode = uint16(lang[0]-0x60)<<10 | uint16(lang[1]-0x60)<<5 | uint16(lang[2]-0x60)
	}
	payload := bytes.Join([][]byte{
		U32(0), U32(0),
		U32(timescale), U32(durationTicks),
		U16(langCode), U16(0),
	}, nil)
	return FullBox("mdhd", 0, 0, payload)
}

// Hdlr builds a handler box.
func Hdlr(handler string, name string) []byte {
	payload := bytes.Join([][]byte{
		U32(0), Str4(handler),
		make([]byte, 12), // reserved
		[]byte(name), []byte{0},
	}, nil)
	return FullBox("hdlr", 0, 0, payload)
}

// Vmhd and Smhd build media information headers.
func Vmhd() []byte { return FullBox("vmhd", 0, 0x000001, U16(0), U16(0), U16(0), U16(0)) }
func Smhd() []byte { return FullBox("smhd", 0, 0, U16(0), U16(0)) }

// Dref/Dinf build a self-contained data reference.
func Dinf() []byte {
	url := FullBox("url ", 0, 0x000001)
	dref := FullBox("dref", 0, 0, U32(1), url)
	return Box("dinf", dref)
}

// Stsd wraps one sample entry.
func Stsd(entry []byte) []byte { return FullBox("stsd", 0, 0, U32(1), entry) }

// Avc1SampleEntry builds a visual sample entry with an avcC configuration.
func Avc1SampleEntry(width, height uint16, avcC []byte) []byte {
	payload := bytes.Join([][]byte{
		make([]byte, 6), U16(1), // reserved, data_reference_index
		U16(0), U16(0), make([]byte, 12), // pre_defined, reserved, pre_defined
		U16(width), U16(height),
		U32(0x00480000), U32(0x00480000), // 72 dpi
		U32(0), U16(1), // reserved, frame_count
		make([]byte, 32), // compressorname
		U16(0x0018), U16(0xFFFF),
		avcC,
	}, nil)
	return Box("avc1", payload)
}

// AvcC builds a minimal AVC decoder configuration record.
func AvcC() []byte {
	// configurationVersion, profile, compat, level, lengthSizeMinusOne=3,
	// numSPS=1, SPS (3 bytes), numPPS=1, PPS (2 bytes).
	body := []byte{1, 0x64, 0x00, 0x1F, 0xFF, 0xE1, 0x00, 0x03, 0x67, 0x64, 0x00, 0x01, 0x00, 0x02, 0x68, 0xEE}
	return Box("avcC", body)
}

// Mp4aSampleEntry builds an audio sample entry with an esds.
func Mp4aSampleEntry(channels uint16, sampleRate uint32, esds []byte) []byte {
	payload := bytes.Join([][]byte{
		make([]byte, 6), U16(1),
		U16(0), U16(0), U32(0), // version, reserved
		U16(channels), U16(16), U16(0), U16(0),
		U32(sampleRate << 16),
		esds,
	}, nil)
	return Box("mp4a", payload)
}

// Esds builds a minimal (opaque) esds payload; its bytes are only carried, not
// interpreted by the sample index.
func Esds() []byte {
	body := []byte{0x03, 0x19, 0x00, 0x01, 0x00, 0x04, 0x11, 0x40, 0x15, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x05, 0x02, 0x12, 0x10, 0x06, 0x01, 0x02}
	return FullBox("esds", 0, 0, body)
}

// Tx3gSampleEntry builds a three-byte-length-prefixed subtitle sample entry.
func Tx3gSampleEntry() []byte {
	payload := bytes.Join([][]byte{
		make([]byte, 6), U16(1),
		U32(0), U32(0), // display flags, justification
		make([]byte, 12), // background, box
		make([]byte, 18), // style record: start, end, font, size, colour
	}, nil)
	return Box("tx3g", payload)
}

// Sample is one media sample: its raw bytes and how many ticks it lasts.
type Sample struct {
	Data          []byte
	DurationTicks uint32
	Sync          bool
}

// SampleTable is the sample description used to build stbl boxes.
type SampleTable struct {
	Entries []Sample
	// Timescale of the sample durations.
	Timescale uint32
}

// Stts encodes run-length sample durations.
func (st *SampleTable) Stts() []byte {
	type run struct{ count, delta uint32 }
	var runs []run
	for _, s := range st.Entries {
		if n := len(runs); n > 0 && runs[n-1].delta == s.DurationTicks {
			runs[n-1].count++
			continue
		}
		runs = append(runs, run{1, s.DurationTicks})
	}
	var payload []byte
	payload = append(payload, U32(uint32(len(runs)))...)
	for _, r := range runs {
		payload = append(payload, U32(r.count)...)
		payload = append(payload, U32(r.delta)...)
	}
	return FullBox("stts", 0, 0, payload)
}

// Stsz encodes sample sizes.
func (st *SampleTable) Stsz() []byte {
	payload := append([]byte{}, U32(0)...) // sample_size uniform = 0
	payload = append(payload, U32(uint32(len(st.Entries)))...)
	for _, s := range st.Entries {
		payload = append(payload, U32(uint32(len(s.Data)))...)
	}
	return FullBox("stsz", 0, 0, payload)
}

// Stsc encodes one sample per chunk (chunk count equals sample count).
func (st *SampleTable) Stsc() []byte {
	payload := append([]byte{}, U32(1)...)
	payload = append(payload, U32(1)...) // first_chunk
	payload = append(payload, U32(1)...) // samples_per_chunk
	payload = append(payload, U32(1)...) // sample_description_index
	return FullBox("stsc", 0, 0, payload)
}

// Stss lists sync sample numbers; when syncSamples is nil every sample is a
// sync sample and stss is omitted by the caller.
func (st *SampleTable) Stss() []byte {
	var nums []uint32
	for i, s := range st.Entries {
		if s.Sync {
			nums = append(nums, uint32(i+1))
		}
	}
	payload := append([]byte{}, U32(uint32(len(nums)))...)
	for _, n := range nums {
		payload = append(payload, U32(n)...)
	}
	return FullBox("stss", 0, 0, payload)
}

// NeedsStss reports whether the table mixes sync and non-sync samples.
func (st *SampleTable) NeedsStss() bool {
	for _, s := range st.Entries {
		if !s.Sync {
			return true
		}
	}
	return false
}

// Stco encodes chunk offsets.
func Stco(offsets []uint32) []byte {
	payload := append([]byte{}, U32(uint32(len(offsets)))...)
	for _, o := range offsets {
		payload = append(payload, U32(o)...)
	}
	return FullBox("stco", 0, 0, payload)
}

// BuildStbl assembles a sample table with chunk offsets resolved to the given
// payload base (the position of the first sample byte in the final file).
func BuildStbl(st *SampleTable, stsdEntry []byte, firstDataOffset uint32) []byte {
	var offsets []uint32
	cur := firstDataOffset
	for _, s := range st.Entries {
		offsets = append(offsets, cur)
		cur += uint32(len(s.Data))
	}
	parts := [][]byte{
		Stsd(stsdEntry),
		st.Stts(),
		st.Stsc(),
		st.Stsz(),
		Stco(offsets),
	}
	if st.NeedsStss() {
		parts = append(parts, st.Stss())
	}
	return Box("stbl", parts...)
}

// FormatBytes renders a short description of a sample for subtitles/SEI.
func FormatBytes(b []byte) string { return fmt.Sprintf("%x", b) }

// EncodeRTPCapture serialises raw RTP packets into the self-describing capture
// format the media package reads: magic "MRTP", packet count, then each packet
// as a 32-bit length and its bytes. It is the fixture counterpart of
// media.ParseRTPCapture.
func EncodeRTPCapture(packets []RTPDatagram) []byte {
	var out []byte
	out = append(out, []byte("MRTP")...)
	out = append(out, U32(uint32(len(packets)))...)
	for _, p := range packets {
		out = append(out, U32(uint32(len(p)))...)
		out = append(out, p...)
	}
	return out
}

// RTPDatagram is one already-marshalled RTP packet.
type RTPDatagram []byte
