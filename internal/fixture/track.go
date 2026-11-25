package fixture

import (
	"bytes"
)

// TrackSpec describes one representation inside a generated file. Each sample
// has a fixed payload size; every SyncInterval-th sample is marked sync.
type TrackSpec struct {
	TrackID      uint32
	Timescale    uint32
	SampleCount  uint32
	SampleDelta  uint32 // duration per sample in the track timescale
	PayloadSize  int
	SyncInterval uint32 // 1 means every sample is a sync sample
	HandlerType  string // e.g. "soun" or "vide"
}

// MediaSpec is the full input for one generated MP4/MOV file.
type MediaSpec struct {
	Brand  string
	Tracks []TrackSpec
}

// ftyp builds a minimal compatible-brands box.
func ftyp(brand string) []byte {
	body := make([]byte, 0, 16)
	body = append(body, brand[0:4]...)
	body = append(body, U32(0)...)
	body = append(body, brand[0:4]...)
	return Box("ftyp", body)
}

func mvhd() []byte {
	body := make([]byte, 0, 100)
	body = append(body, U32(0)...)                 // creation
	body = append(body, U32(0)...)                 // modification
	body = append(body, U32(TickTimescale)...)     // movie timescale
	body = append(body, U32(0)...)                 // duration
	body = append(body, U32(0x00010000)...)        // rate 1.0
	body = append(body, 0x01, 0x00)                // volume
	body = append(body, make([]byte, 10)...)       // reserved
	body = append(body, make([]byte, 36)...)       // identity matrix
	body = append(body, make([]byte, 24)...)       // pre-defined
	body = append(body, U32(2)...)                 // next track ID
	return FullBox("mvhd", 0, 0, body)
}

// Track builds one trak tree and returns its sample byte offsets in the order
// samples will be appended to mdat.
func (s TrackSpec) trak() []byte {
	mdhd := FullBox("mdhd", 0, 0, bytes.Join([][]byte{
		U32(0), U32(0), U32(s.Timescale),
		U32(s.SampleCount * s.SampleDelta),
		{0x55, 0xc4, 0x00, 0x00}, // und language, pre-defined
	}, nil))

	hdlrBody := make([]byte, 0, 32)
	hdlrBody = append(hdlrBody, U32(0)...)
	hdlrBody = append(hdlrBody, s.HandlerType[0:4]...)
	hdlrBody = append(hdlrBody, make([]byte, 12)...)
	hdlrBody = append(hdlrBody, []byte("name\x00")...)
	hdlr := FullBox("hdlr", 0, 0, hdlrBody)
	minf := Box("minf", Box("stbl", s.stbl()))
	mdia := Box("mdia", bytes.Join([][]byte{mdhd, hdlr, minf}, nil))

	tkhd := FullBox("tkhd", 0, 0x3, bytes.Join([][]byte{
		U32(0), U32(0), U32(s.TrackID), U32(0), U32(0),
		U32(0), U32(0), make([]byte, 2),
		U16(0), U16(0), make([]byte, 36),
		U32(0x00010000), U32(0), U32(0),
		U32(0x00010000), U32(0), U32(0),
		U32(0x00010000), U32(0x00400000),
	}, nil))
	return Box("trak", append(tkhd, mdia...))
}

// stbl builds the sample table. stsc uses one sample per chunk, stco entries
// are placeholders patched by the caller once the mdat position is known.
func (s TrackSpec) stbl() []byte {
	stts := FullBox("stts", 0, 0, append(U32(1),
		append(U32(s.SampleCount), U32(s.SampleDelta)...)...))
	stsc := FullBox("stsc", 0, 0, bytes.Join([][]byte{
		U32(1), U32(1), U32(1), U32(1),
	}, nil))
	stszHead := append(U32(0), U32(s.SampleCount)...)
	stsz := FullBox("stsz", 0, 0, append(stszHead, stszSizes(s.SampleCount, s.PayloadSize)...))
	stco := FullBox("stco", 0, 0, append(U32(s.SampleCount),
		make([]byte, int(s.SampleCount)*4)...))
	boxes := [][]byte{stts, stsc, stsz, stco}
	if s.SyncInterval > 0 {
		syncs := make([]uint32, 0, s.SampleCount)
		for i := uint32(1); i <= s.SampleCount; i++ {
			if (i-1)%s.SyncInterval == 0 {
				syncs = append(syncs, i)
			}
		}
		body := U32(uint32(len(syncs)))
		for _, n := range syncs {
			body = append(body, U32(n)...)
		}
		boxes = append(boxes, FullBox("stss", 0, 0, body))
	}
	return bytes.Join(boxes, nil)
}

func stszSizes(count uint32, payloadSize int) []byte {
	out := make([]byte, int(count)*4)
	for i := range out {
		out[i] = 0
	}
	for i := uint32(0); i < count; i++ {
		binaryPutU32(out[int(i)*4:], uint32(SampleHeaderSize+payloadSize))
	}
	return out
}

func binaryPutU32(b []byte, v uint32) { b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v) }

// TickTimescale is exposed for callers building mvhd-equivalent boxes.
const TickTimescale = 90000
