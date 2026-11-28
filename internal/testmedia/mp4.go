package testmedia

import (
	"bytes"
	"encoding/binary"
)

// TrackData carries the generated sample payloads alongside their spec so
// downstream tests can hash expected bytes without reparsing the container.
type TrackData struct {
	Spec    TrackSpec
	Samples [][]byte
}

// BuiltMedia is a serialized container plus the per-track sample bytes used
// to build it.
type BuiltMedia struct {
	Container []byte
	Tracks    []TrackData
}

// chunk is one stco entry: sample's absolute offset relative to the file.
type chunk struct {
	track  int
	sample int
	offset uint32
}

// BuildMP4 serializes spec into a minimal but standards-shaped MP4: ftyp,
// moov (mvhd/trak[tkhd,mdhd,hdlr,minf/stbl]) and a single mdat holding every
// sample contiguously. One sample per chunk keeps the chunk offset table
// explicit and easy to verify.
func BuildMP4(spec MediaSpec) (*BuiltMedia, error) {
	media := &BuiltMedia{}

	var mdat bytes.Buffer
	chunks := make([][]chunk, len(spec.Tracks))

	for ti, tr := range spec.Tracks {
		data := TrackData{Spec: tr}
		for si := 0; si < tr.Samples; si++ {
			payload := SampleBytes(tr.Seed, si, tr.SampleSize)
			data.Samples = append(data.Samples, payload)
			chunks[ti] = append(chunks[ti], chunk{
				track: ti, sample: si, offset: uint32(mdat.Len()),
			})
			mdat.Write(payload)
		}
		media.Tracks = append(media.Tracks, data)
	}

	moov := buildMoov(spec, chunks, uint32(mdat.Len()))
	ftyp := encodeBox(fourcc("ftyp"), buildFtyp())
	mdatBox := encodeBox(fourcc("mdat"), mdat.Bytes())

	// mdat payload begins right after ftyp + moov + the 8-byte mdat header;
	// chunk offsets stored above are relative to mdat payload start.
	base := uint32(len(ftyp) + len(moov) + 8)
	for ti := range chunks {
		for ci := range chunks[ti] {
			chunks[ti][ci].offset += base
		}
	}
	// Rebuild moov now that absolute offsets are known.
	moov = buildMoov(spec, chunks, uint32(mdat.Len()))

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(moov)
	out.Write(mdatBox)
	media.Container = out.Bytes()
	return media, nil
}

func buildFtyp() []byte {
	var b bytes.Buffer
	b.Write([]byte("isom"))             // major brand
	b.Write(u32b(0x200))                // minor version
	b.Write([]byte("isomiso2mp41qt  ")) // compatible brands (12 bytes)
	return b.Bytes()
}

func buildMoov(spec MediaSpec, chunks [][]chunk, mdatSize uint32) []byte {
	w := NewBoxWriter()
	w.Open("moov")
	w.Open("mvhd")
	w.Write(make([]byte, 4)) // version+flags
	w.U32(0)                 // creation time
	w.U32(0)                 // modification time
	w.U32(spec.MovieTimescale)
	maxDur := uint32(0)
	for _, tr := range spec.Tracks {
		if d := tr.Delta * uint32(tr.Samples); d > maxDur {
			maxDur = d
		}
	}
	w.U32(maxDur)
	w.Write(make([]byte, 80)) // rate/volume/matrix/predefined (fixed mvhd tail)
	w.U32(2)                  // next track ID
	w.Close()

	for ti, tr := range spec.Tracks {
		w.Open("trak")
		w.Open("tkhd")
		w.Write(make([]byte, 4)) // version+flags
		w.U32(0)                 // creation
		w.U32(0)                 // modification
		w.U32(uint32(ti + 1))    // track ID
		w.U32(0)                 // reserved
		w.U32(tr.Delta * uint32(tr.Samples))
		w.Write(make([]byte, 8))  // reserved
		w.U16(0)                  // layer
		w.U16(0)                  // alt group
		w.U16(0)                  // volume
		w.U16(0)                  // reserved
		w.Write(identityMatrix()) // 36 bytes
		w.U32(320 << 16)          // width  (16.16)
		w.U32(240 << 16)          // height (16.16)
		w.Close()

		w.Open("mdia")
		w.Open("mdhd")
		w.Write(make([]byte, 4))
		w.U32(0)
		w.U32(0)
		w.U32(tr.Timescale)
		w.U32(tr.Delta * uint32(tr.Samples))
		w.U16(0x55c4) // packed language 'und'
		w.U16(0)      // pre-defined
		w.Close()

		w.Open("hdlr")
		w.Write(make([]byte, 4))
		w.U32(0) // pre-defined
		handler := "vide"
		if tr.Kind == KindAudio {
			handler = "soun"
		}
		w.Write([]byte(handler))
		w.Write(make([]byte, 12)) // reserved
		w.Write([]byte("synthetic " + handler + " track\x00"))
		w.Close()

		w.Open("minf")
		buildMediaHeader(w, tr)
		w.Open("dinf")
		w.Open("dref")
		w.Write(make([]byte, 4))
		w.U32(1) // entry count
		w.Open("url ")
		w.U32(1) // self-contained flag
		w.Close()
		w.Close() // dref
		w.Close() // dinf
		buildStbl(w, tr, chunks[ti])
		w.Close() // minf
		w.Close() // mdia
		w.Close() // trak
	}
	w.Close() // moov
	return w.Bytes()
}

func buildMediaHeader(w *BoxWriter, tr TrackSpec) {
	if tr.Kind == KindVideo {
		w.Open("vmhd")
		w.U32(1) // version 0, flags=1
		w.Write(make([]byte, 8))
		w.Close()
		return
	}
	w.Open("smhd")
	w.Write(make([]byte, 4))
	w.U16(0) // balance
	w.U16(0) // reserved
	w.Close()
}

func buildStbl(w *BoxWriter, tr TrackSpec, ch []chunk) {
	w.Open("stbl")

	// stsd: one sample entry. Only the fourCC and fixed sample-entry framing
	// matter for parsing; codec-private config is intentionally minimal.
	w.Open("stsd")
	w.Write(make([]byte, 4))
	w.U32(1)
	w.Open(tr.Codec)
	if tr.Kind == KindVideo {
		w.Write(make([]byte, 6))  // reserved
		w.U16(1)                  // data reference index
		w.U16(0)                  // pre-defined
		w.U16(0)                  // reserved
		w.Write(make([]byte, 12)) // pre-defined[3]
		w.U16(320)                // width
		w.U16(240)                // height
		w.U32(0x00480000)         // horizontal resolution 72dpi
		w.U32(0x00480000)         // vertical resolution
		w.U32(0)                  // reserved
		w.U16(1)                  // frame count
		var name [32]byte
		w.Write(name[:])
		w.U16(0x0018) // depth
		w.U16(0xffff) // pre-defined
	} else {
		w.Write(make([]byte, 6)) // reserved
		w.U16(1)                 // data reference index
		w.U16(0)                 // entry version
		w.Write(make([]byte, 6)) // reserved[3]
		w.U16(1)                 // channel count
		w.U16(16)                // sample size
		w.U16(0)                 // pre-defined
		w.U16(0)                 // reserved
		w.U32(tr.Timescale << 16)
	}
	w.Close() // codec entry
	w.Close() // stsd

	// stts: constant delta.
	w.Open("stts")
	w.Write(make([]byte, 4))
	w.U32(1)
	w.U32(uint32(tr.Samples))
	w.U32(tr.Delta)
	w.Close()

	// stss: sync samples for video only.
	if tr.Kind == KindVideo {
		w.Open("stss")
		w.Write(make([]byte, 4))
		var keys []uint32
		for si := 1; si <= tr.Samples; si++ {
			if tr.KeyframeEvery == 0 || (si-1)%tr.KeyframeEvery == 0 {
				keys = append(keys, uint32(si))
			}
		}
		w.U32(uint32(len(keys)))
		for _, k := range keys {
			w.U32(k)
		}
		w.Close()
	}

	// stsc: one sample per chunk, description index 1.
	w.Open("stsc")
	w.Write(make([]byte, 4))
	w.U32(1)
	w.U32(1) // first chunk
	w.U32(1) // samples per chunk
	w.U32(1) // sample description index
	w.Close()

	// stsz
	w.Open("stsz")
	w.Write(make([]byte, 4))
	w.U32(0) // sample size 0 -> table present
	w.U32(uint32(tr.Samples))
	for si := 0; si < tr.Samples; si++ {
		w.U32(uint32(tr.SampleSize))
	}
	w.Close()

	// stco
	w.Open("stco")
	w.Write(make([]byte, 4))
	w.U32(uint32(len(ch)))
	for _, c := range ch {
		w.U32(c.offset)
	}
	w.Close()

	w.Close() // stbl
}

func identityMatrix() []byte {
	m := make([]byte, 36)
	binary.BigEndian.PutUint32(m[0:], 0x00010000)
	binary.BigEndian.PutUint32(m[16:], 0x00010000)
	binary.BigEndian.PutUint32(m[32:], 0x40000000)
	return m
}
