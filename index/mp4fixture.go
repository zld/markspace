package index

import (
	"encoding/binary"
)

// MP4FixtureConfig configures a small deterministic MP4 byte stream.
type MP4FixtureConfig struct {
	SampleCount int
	SyncSamples []uint32
	OmitSTSS    bool
	Corrupt     map[uint32]bool
	Timescale   uint32
	SampleDelta uint32
	SampleSize  uint32
}

// BuildMP4Fixture builds a minimal but go-mp4-readable MP4 with one chunk and
// deterministic sample markers. The returned bytes are generated test data, not
// external media.
func BuildMP4Fixture(cfg MP4FixtureConfig) []byte {
	if cfg.SampleCount <= 0 {
		cfg.SampleCount = 8
	}
	if cfg.Timescale == 0 {
		cfg.Timescale = 1000
	}
	if cfg.SampleDelta == 0 {
		cfg.SampleDelta = 40 // 25 fps in the millisecond time base
	}
	if cfg.SampleSize == 0 {
		cfg.SampleSize = 16
	}

	samples := make([]byte, 0, cfg.SampleCount*int(cfg.SampleSize))
	for n := uint32(1); n <= uint32(cfg.SampleCount); n++ {
		marker := DeltaMarker
		for _, sync := range cfg.SyncSamples {
			if sync == n {
				marker = KeyMarker
				break
			}
		}
		if cfg.Corrupt[n] {
			marker = []byte("BAD!")
		}
		row := make([]byte, cfg.SampleSize)
		copy(row, marker)
		for i := 4; i < len(row); i++ {
			row[i] = byte(n*29 + uint32(i))
		}
		samples = append(samples, row...)
	}

	ftyp := boxBytes("ftyp", concat(
		fourCC("isom"),
		u32(0x200),
		fourCC("isom"),
		fourCC("iso2"),
	))

	mdhdPayload := concat(
		u32(0), // version + flags
		u32(0), u32(0),
		u32(cfg.Timescale),
		u32(uint32(cfg.SampleCount)*cfg.SampleDelta),
		u16(0x55c4), // language "und"-style padding
		u16(0),
	)
	mdhd := boxBytes("mdhd", mdhdPayload)
	hdlr := boxBytes("hdlr", concat(
		u32(0),
		u32(0),
		fourCC("vide"),
		make([]byte, 12),
		[]byte("VideoHandler\x00"),
	))

	stts := boxBytes("stts", concat(
		u32(0),
		u32(1),
		u32(uint32(cfg.SampleCount)),
		u32(cfg.SampleDelta),
	))
	var stss []byte
	if !cfg.OmitSTSS {
		nums := make([]byte, 4*len(cfg.SyncSamples))
		for i, n := range cfg.SyncSamples {
			binary.BigEndian.PutUint32(nums[i*4:], n)
		}
		stss = boxBytes("stss", concat(u32(0), u32(uint32(len(cfg.SyncSamples))), nums))
	}
	stsc := boxBytes("stsc", concat(
		u32(0), u32(1),
		u32(1), u32(uint32(cfg.SampleCount)), u32(1),
	))
	sizes := make([]byte, 4*cfg.SampleCount)
	for i := 0; i < cfg.SampleCount; i++ {
		binary.BigEndian.PutUint32(sizes[i*4:], cfg.SampleSize)
	}
	stsz := boxBytes("stsz", concat(
		u32(0), u32(0), u32(uint32(cfg.SampleCount)), sizes,
	))
	stco := boxBytes("stco", concat(u32(0), u32(1), u32(0))) // offset patched below
	stbl := boxBytes("stbl", concat(stts, stss, stsc, stsz, stco))
	minf := boxBytes("minf", stbl)
	mdia := boxBytes("mdia", concat(mdhd, hdlr, minf))
	trak := boxBytes("trak", mdia)
	moov := boxBytes("moov", trak)
	mdat := boxBytes("mdat", samples)

	out := concat(ftyp, moov, mdat)
	mdatPayloadStart := uint32(len(ftyp) + len(moov) + 8)
	patchSTCOOffset(out, mdatPayloadStart)
	return out
}

func patchSTCOOffset(data []byte, offset uint32) {
	for i := 0; i+12 < len(data)-4; i++ {
		if string(data[i:i+4]) == "stco" {
			// [size(4)][type(4)="stco"][version/flags(4)][count(4)][offset(4)]
			binary.BigEndian.PutUint32(data[i+12:i+16], offset)
			return
		}
	}
}

func boxBytes(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func fourCC(s string) []byte {
	b := []byte("    ")
	copy(b, s)
	return b
}
