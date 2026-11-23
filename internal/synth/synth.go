// Package synth builds deterministic CMAF/fMP4, PCM and RTP fixtures in
// memory. Nothing here reads the network or the filesystem; the same seed
// always yields byte-identical input, which is what makes checkpoint replay
// and manifest comparison meaningful.
package synth

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/abema/go-mp4"
)

// memFile is a minimal io.WriteSeeker used by mp4.Writer.
type memFile struct {
	buf []byte
	pos int64
}

func (m *memFile) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		m.buf = append(m.buf, make([]byte, end-int64(len(m.buf)))...)
	}
	copy(m.buf[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = off
	case io.SeekCurrent:
		m.pos += off
	case io.SeekEnd:
		m.pos = int64(len(m.buf)) + off
	}
	if m.pos < 0 {
		return 0, errors.New("synth: negative seek")
	}
	return m.pos, nil
}

// boxWriter appends boxes with automatically computed sizes.
type boxWriter struct {
	mem memFile
}

func newBoxWriter() *boxWriter { return &boxWriter{} }

// container writes a box whose payload is filled by fn.
func (b *boxWriter) container(typ string, fn func() error) error {
	if _, err := b.mem.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	start := b.mem.pos
	var header [8]byte
	binary.BigEndian.PutUint32(header[:4], 1) // patched below
	copy(header[4:], typ)
	if _, err := b.mem.Write(header[:]); err != nil {
		return err
	}
	if fn != nil {
		if err := fn(); err != nil {
			return err
		}
	}
	end := b.mem.pos
	size := end - start
	if size < 8 || size > math.MaxUint32 {
		return fmt.Errorf("synth: box %q size %d out of range", typ, size)
	}
	if _, err := b.mem.Seek(start, io.SeekStart); err != nil {
		return err
	}
	binary.BigEndian.PutUint32(header[:4], uint32(size))
	if _, err := b.mem.Write(header[:]); err != nil {
		return err
	}
	_, err := b.mem.Seek(end, io.SeekStart)
	return err
}

// full writes a full box: size, type, version/flags, raw payload.
func (b *boxWriter) full(typ string, version byte, flags uint32, payload []byte) error {
	var header [12]byte
	binary.BigEndian.PutUint32(header[:4], uint32(12+len(payload)))
	copy(header[4:], typ)
	header[8] = version
	header[9] = byte(flags >> 16)
	header[10] = byte(flags >> 8)
	header[11] = byte(flags)
	if _, err := b.mem.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err := b.mem.Write(header[:]); err != nil {
		return err
	}
	_, err := b.mem.Write(payload)
	return err
}

// raw writes a plain box (no version/flags) with a raw payload.
func (b *boxWriter) raw(typ string, payload []byte) error {
	var header [8]byte
	binary.BigEndian.PutUint32(header[:4], uint32(8+len(payload)))
	copy(header[4:], typ)
	if _, err := b.mem.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err := b.mem.Write(header[:]); err != nil {
		return err
	}
	_, err := b.mem.Write(payload)
	return err
}

// marshal writes a go-mp4 box payload into the buffer.
func (b *boxWriter) marshal(box mp4.IImmutableBox) error {
	if _, err := b.mem.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	_, err := mp4.Marshal(&b.mem, box, mp4.Context{})
	return err
}

func (b *boxWriter) bytes() []byte { return b.mem.buf }

// TrackSpec describes one track in the generated init segment.
type TrackSpec struct {
	ID        uint32
	Handler   string // "vide" or "soun"
	Timescale uint32
	Width     uint32
	Height    uint32
	Channels  uint16
}

// VideoTrack and AudioTrack are the fixture timebases.
func VideoTrack(id uint32) TrackSpec {
	return TrackSpec{ID: id, Handler: "vide", Timescale: 90000, Width: 1280, Height: 720}
}

func AudioTrack(id uint32) TrackSpec {
	return TrackSpec{ID: id, Handler: "soun", Timescale: 48000, Channels: 2}
}

// InitSegment builds ftyp+moov describing every track.
func InitSegment(movieTimescale uint32, tracks []TrackSpec) ([]byte, error) {
	w := newBoxWriter()
	err := w.container("ftyp", func() error {
		w.mem.Write([]byte("isom"))
		w.mem.Write([]byte{0, 0, 2, 0})
		w.mem.Write([]byte("isom"))
		w.mem.Write([]byte("iso6"))
		w.mem.Write([]byte("cmfc"))
		return nil
	})
	if err != nil {
		return nil, err
	}
	var built error
	err = w.container("moov", func() error {
		if err := func() error {
			// mvhd v0 body: creation, modification, timescale, duration,
			// rate, volume+reserved, 2xreserved, matrix, 6xpredefined,
			// next_track_id = 96 bytes.
			payload := make([]byte, 96)
			binary.BigEndian.PutUint32(payload[0:], 0)
			binary.BigEndian.PutUint32(payload[4:], 0)
			binary.BigEndian.PutUint32(payload[8:], movieTimescale)
			binary.BigEndian.PutUint32(payload[12:], 0)
			binary.BigEndian.PutUint32(payload[16:], 0x00010000)
			binary.BigEndian.PutUint16(payload[20:], 0x0100)
			for i, v := range []int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
				binary.BigEndian.PutUint32(payload[32+i*4:], uint32(v))
			}
			binary.BigEndian.PutUint32(payload[92:], uint32(len(tracks))+1)
			return w.full("mvhd", 0, 0, payload)
		}(); err != nil {
			return err
		}
		for _, t := range tracks {
			spec := t
			if err := w.container("trak", func() error { return buildTrak(w, spec) }); err != nil {
				return err
			}
		}
		if err := w.container("mvex", func() error {
			for _, t := range tracks {
				payload := make([]byte, 20)
				binary.BigEndian.PutUint32(payload[0:], t.ID)
				binary.BigEndian.PutUint32(payload[4:], 1)
				if err := w.full("trex", 0, 0, payload); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		built = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	if built != nil {
		return nil, built
	}
	return w.bytes(), nil
}

func buildTrak(w *boxWriter, t TrackSpec) error {
	volume := uint16(0)
	if t.Handler == "soun" {
		volume = 0x0100
	}
	// mp4.Marshal emits the payload only, so each box needs its own header.
	if err := w.container("tkhd", func() error {
		return w.marshal(&mp4.Tkhd{
			FullBox: mp4.FullBox{Version: 0},
			TrackID: t.ID,
			Volume:  int16(volume),
			Width:   t.Width << 16,
			Height:  t.Height << 16,
		})
	}); err != nil {
		return err
	}
	return w.container("mdia", func() error {
		if err := w.container("mdhd", func() error {
			return w.marshal(&mp4.Mdhd{
				FullBox:   mp4.FullBox{Version: 0},
				Timescale: t.Timescale,
			})
		}); err != nil {
			return err
		}
		var handler [4]byte
		copy(handler[:], t.Handler)
		if err := w.container("hdlr", func() error {
			return w.marshal(&mp4.Hdlr{
				HandlerType: handler,
				Name:        "avbatch",
			})
		}); err != nil {
			return err
		}
		return w.container("minf", func() error {
			return w.container("stbl", func() error { return nil })
		})
	})
}

// Sample is one fixture sample: payload size and decode duration.
type Sample struct {
	Size     uint32
	Duration uint32
}

// FragmentSpec is one moof+mdat to build.
type FragmentSpec struct {
	Sequence        uint32
	TrackID         uint32
	BaseDecodeTime  uint64
	Samples         []Sample
	Timescale       uint32
	OmitSequence    bool
	TruncateAt      int64 // >0 writes only this many bytes (fault injection)
	CompositionTime int32
}

// Fragment builds the moof+mdat bytes. Sample payloads are deterministic,
// derived from sequence, track and index so duplicate detection has real input.
func Fragment(spec FragmentSpec) ([]byte, error) {
	if len(spec.Samples) == 0 {
		return nil, errors.New("synth: fragment needs at least one sample")
	}
	w := newBoxWriter()
	// Layout constants for the single trun this fragment writes.
	trunSize := 12 + 4 + 4 + len(spec.Samples)*8
	_ = trunSize
	err := w.container("moof", func() error {
		if !spec.OmitSequence {
			payload := make([]byte, 4)
			binary.BigEndian.PutUint32(payload, spec.Sequence)
			if err := w.full("mfhd", 0, 0, payload); err != nil {
				return err
			}
		}
		return w.container("traf", func() error {
			tfhd := make([]byte, 8)
			binary.BigEndian.PutUint32(tfhd[0:], spec.TrackID)
			binary.BigEndian.PutUint32(tfhd[4:], 0x010000) // default-base-is-moof
			if err := w.full("tfhd", 0, 0x020000, tfhd); err != nil {
				return err
			}
			tfdt := make([]byte, 8)
			binary.BigEndian.PutUint64(tfdt, spec.BaseDecodeTime)
			if err := w.full("tfdt", 1, 0, tfdt); err != nil {
				return err
			}
			// trun with data-offset, per-sample duration and size.
			// moof size is not known yet, so reserve a fixed-size trun and
			// patch the data offset after moof is closed.
			buf := make([]byte, trunSize)
			binary.BigEndian.PutUint32(buf[0:], uint32(trunSize))
			copy(buf[4:], "trun")
			buf[8] = 0
			flags := uint32(0x001 | 0x100 | 0x200)
			buf[9], buf[10], buf[11] = byte(flags>>16), byte(flags>>8), byte(flags)
			binary.BigEndian.PutUint32(buf[12:], uint32(len(spec.Samples)))
			// data offset placeholder at [16:20]
			for i, s := range spec.Samples {
				binary.BigEndian.PutUint32(buf[20+i*8:], s.Duration)
				binary.BigEndian.PutUint32(buf[24+i*8:], s.Size)
			}
			if _, err := w.mem.Seek(0, io.SeekEnd); err != nil {
				return err
			}
			if _, err := w.mem.Write(buf); err != nil {
				return err
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	moofSize := w.mem.pos
	// Patch the trun data offset: relative to the moof start. The trun is the
	// last box in the moof, so it ends exactly at moofSize, and its data
	// offset field sits at +16 from the trun box start (size+type+ver/flags+
	// sample_count).
	offsetPos := moofSize - int64(trunSize) + 16
	binary.BigEndian.PutUint32(w.mem.buf[offsetPos:], uint32(moofSize+8))
	err = w.container("mdat", func() error {
		for i, s := range spec.Samples {
			payload := samplePayload(spec.Sequence, spec.TrackID, i, s.Size)
			if _, err := w.mem.Write(payload); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := w.bytes()
	if spec.TruncateAt > 0 && int64(len(out)) > spec.TruncateAt {
		out = out[:spec.TruncateAt]
	}
	return out, nil
}

// samplePayload fills a sample with a deterministic, mostly incompressible
// pattern so digests are meaningful.
func samplePayload(seq, track uint32, idx int, size uint32) []byte {
	buf := make([]byte, size)
	var state uint64 = 0x9E3779B97F4A7C15 ^ uint64(seq)*0x100000001B3 ^ uint64(track)<<32 ^ uint64(idx)
	for i := range buf {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		buf[i] = byte(state)
	}
	if size >= 4 {
		binary.BigEndian.PutUint32(buf, seq*1000+track*10+uint32(idx))
	}
	return buf
}

// AppendedSamplePayload is exported so the pipeline can slice real bytes out of
// a fragment without duplicating the generator.
func AppendedSamplePayload(seq, track uint32, idx int, size uint32) []byte {
	return samplePayload(seq, track, idx, size)
}
