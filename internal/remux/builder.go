package remux

import (
	"bytes"
	"encoding/binary"

	"mediacheck/internal/demux"
	"mediacheck/internal/testmedia"
)

// builder accumulates verbatim per-track samples and rebuilds a
// standards-shaped MP4 with a low-level box writer. The remuxer never decodes
// or re-encodes: every payload byte is the exact source sample.
type builder struct {
	tracks []trackPayload
}

type trackPayload struct {
	track   *demux.Track
	samples [][]byte
}

// loc is one sample's offset/size inside the output container.
type loc struct{ offset, size uint32 }

func newBuilder() *builder { return &builder{} }

func (b *builder) addSample(tr *demux.Track, payload []byte) {
	for i := range b.tracks {
		if b.tracks[i].track == tr {
			b.tracks[i].samples = append(b.tracks[i].samples, payload)
			return
		}
	}
	b.tracks = append(b.tracks, trackPayload{track: tr, samples: [][]byte{payload}})
}

// finish lays out ftyp + moov + mdat. Chunk offsets are computed against the
// real (variable-size) payloads and patched into the moov before serialization.
func (b *builder) finish(c *demux.Container) ([]byte, error) {
	ftyp := box("ftyp", ftypPayload())

	// First pass: concatenate mdat payload and remember per-sample offsets
	// relative to the mdat payload start.
	var mdat bytes.Buffer
	locations := make([][]loc, len(b.tracks))
	for ti, tp := range b.tracks {
		for _, p := range tp.samples {
			locations[ti] = append(locations[ti], loc{
				offset: uint32(mdat.Len()),
				size:   uint32(len(p)),
			})
			mdat.Write(p)
		}
	}

	moov := b.buildMoov(c, locations, uint32(mdat.Len()))
	mdatBox := box("mdat", mdat.Bytes())

	// mdat payload starts after ftyp, moov and the 8-byte mdat header.
	base := uint32(len(ftyp) + len(moov) + 8)
	for ti := range locations {
		for i := range locations[ti] {
			locations[ti][i].offset += base
		}
	}
	moov = b.buildMoov(c, locations, uint32(mdat.Len()))

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(moov)
	out.Write(mdatBox)
	return out.Bytes(), nil
}

func (b *builder) buildMoov(c *demux.Container, locations [][]loc, mdatSize uint32) []byte {
	w := testmedia.NewBoxWriter()
	w.Open("moov")
	w.Open("mvhd")
	w.Write(make([]byte, 4))
	w.U32(0)
	w.U32(0)
	w.U32(c.MovieTimescale)
	w.U32(uint32(maxDuration(c)))
	w.Write(make([]byte, 80))
	w.U32(uint32(len(b.tracks) + 1))
	w.Close()

	for ti, tp := range b.tracks {
		tr := tp.track
		w.Open("trak")
		w.Open("tkhd")
		w.Write(make([]byte, 4))
		w.U32(0)
		w.U32(0)
		w.U32(tr.ID)
		w.U32(0)
		w.U32(uint32(tr.Duration))
		w.Write(make([]byte, 8))
		w.U16(0)
		w.U16(0)
		w.U16(0)
		w.U16(0)
		w.Write(identityMatrix())
		w.U32(uint32(tr.Width) << 16)
		w.U32(uint32(tr.Height) << 16)
		w.Close()

		w.Open("mdia")
		w.Open("mdhd")
		w.Write(make([]byte, 4))
		w.U32(0)
		w.U32(0)
		w.U32(tr.Timescale)
		w.U32(uint32(tr.Duration))
		w.U16(0x55c4)
		w.U16(0)
		w.Close()

		w.Open("hdlr")
		w.Write(make([]byte, 4))
		w.U32(0)
		handler := "vide"
		if tr.Kind == demux.KindAudio {
			handler = "soun"
		}
		w.Write([]byte(handler))
		w.Write(make([]byte, 12))
		w.Write([]byte("remuxed " + handler + "\x00"))
		w.Close()

		w.Open("minf")
		writeMediaHeader(w, tr)
		w.Open("dinf")
		w.Open("dref")
		w.Write(make([]byte, 4))
		w.U32(1)
		w.Open("url ")
		w.U32(1)
		w.Close()
		w.Close()
		w.Close()
		writeStbl(w, tr, tp.samples, locations[ti])
		w.Close() // minf
		w.Close() // mdia
		w.Close() // trak
		_ = ti
	}
	w.Close()
	return w.Bytes()
}

func writeStbl(w *testmedia.BoxWriter, tr *demux.Track, samples [][]byte, locs []loc) {
	w.Open("stbl")
	writeStsd(w, tr)

	// stts from the original (possibly non-constant) durations.
	w.Open("stts")
	w.Write(make([]byte, 4))
	entries := sttsEntries(tr)
	w.U32(uint32(len(entries)))
	for _, e := range entries {
		w.U32(e.count)
		w.U32(e.delta)
	}
	w.Close()

	if tr.Kind == demux.KindVideo {
		w.Open("stss")
		w.Write(make([]byte, 4))
		var keys []uint32
		for i, s := range tr.Samples {
			if s.Sync {
				keys = append(keys, uint32(i+1))
			}
		}
		w.U32(uint32(len(keys)))
		for _, k := range keys {
			w.U32(k)
		}
		w.Close()
	}

	w.Open("stsc")
	w.Write(make([]byte, 4))
	w.U32(1)
	w.U32(1)
	w.U32(1)
	w.U32(1)
	w.Close()

	// stsz with the real per-sample sizes.
	w.Open("stsz")
	w.Write(make([]byte, 4))
	w.U32(0)
	w.U32(uint32(len(samples)))
	for _, p := range samples {
		w.U32(uint32(len(p)))
	}
	w.Close()

	w.Open("stco")
	w.Write(make([]byte, 4))
	w.U32(uint32(len(locs)))
	for _, l := range locs {
		w.U32(l.offset)
	}
	w.Close()
	w.Close()
}

type sttsEntry struct{ count, delta uint32 }

// sttsEntries run-length compresses the parsed durations back into stts rows.
func sttsEntries(tr *demux.Track) []sttsEntry {
	var out []sttsEntry
	for _, s := range tr.Samples {
		d := uint32(s.Duration)
		if n := len(out); n > 0 && out[n-1].delta == d {
			out[n-1].count++
			continue
		}
		out = append(out, sttsEntry{count: 1, delta: d})
	}
	return out
}

func writeStsd(w *testmedia.BoxWriter, tr *demux.Track) {
	w.Open("stsd")
	w.Write(make([]byte, 4))
	w.U32(1)
	w.Open(tr.Codec)
	if tr.Kind == demux.KindVideo {
		w.Write(make([]byte, 6))
		w.U16(1)
		w.U16(0)
		w.U16(0)
		w.Write(make([]byte, 12))
		w.U16(tr.Width)
		w.U16(tr.Height)
		w.U32(0x00480000)
		w.U32(0x00480000)
		w.U32(0)
		w.U16(1)
		w.Write(make([]byte, 32))
		w.U16(0x0018)
		w.U16(0xffff)
	} else {
		w.Write(make([]byte, 6))
		w.U16(1)
		w.U16(0)
		w.Write(make([]byte, 6))
		w.U16(tr.Channels)
		w.U16(16)
		w.U16(0)
		w.U16(0)
		w.U32(tr.SampleRate)
	}
	w.Close()
	w.Close()
}

func writeMediaHeader(w *testmedia.BoxWriter, tr *demux.Track) {
	if tr.Kind == demux.KindVideo {
		w.Open("vmhd")
		w.U32(1)
		w.Write(make([]byte, 8))
		w.Close()
		return
	}
	w.Open("smhd")
	w.Write(make([]byte, 4))
	w.U16(0)
	w.U16(0)
	w.Close()
}

func maxDuration(c *demux.Container) int64 {
	m := int64(0)
	for _, tr := range c.Tracks {
		if tr.Duration > m {
			m = tr.Duration
		}
	}
	return m
}

func box(typ string, payload []byte) []byte {
	buf := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(8+len(payload)))
	copy(buf[4:], []byte(typ))
	return append(buf, payload...)
}

func ftypPayload() []byte {
	var b bytes.Buffer
	b.Write([]byte("isom"))
	b.Write(u32(0x200))
	b.Write([]byte("isomiso2mp41qt  "))
	return b.Bytes()
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func identityMatrix() []byte {
	m := make([]byte, 36)
	binary.BigEndian.PutUint32(m[0:], 0x00010000)
	binary.BigEndian.PutUint32(m[16:], 0x00010000)
	binary.BigEndian.PutUint32(m[32:], 0x40000000)
	return m
}
