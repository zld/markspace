package mp4

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"mediapipe/internal/meta"
)

// Writer limits mirror the parser bounds so the project can never emit a
// fixture it would reject.
const (
	MaxWriteTracks  = MaxTrackCount
	MaxWriteSamples = MaxSamplesPerSeg
)

// TrackSpec declares one track for fixture generation.
type TrackSpec struct {
	ID         uint32
	Handler    string // "vide" or "soun"
	Timescale  uint32
	Language   string
	Width      uint16
	Height     uint16
	Color      *Colr
	Channels   uint16
	SampleSize uint16
	SampleRate uint32
}

// SampleSpec carries one sample's payload and timing.
type SampleSpec struct {
	TrackID  uint32
	Duration uint32
	CTSDelta int32
	Sync     bool
	Payload  []byte
}

// SegmentSpec is one fragment to emit. DTS maps track id to the fragment's
// base media decode time; missing entries start at zero.
type SegmentSpec struct {
	Sequence uint32
	DTS      map[uint32]uint64
	Samples  []SampleSpec
	Emsg     []meta.Emsg
}

// BuildFile serializes a complete fragmented MP4 fixture: ftyp, moov with
// declared tracks and empty sample tables, then for each segment (emsg*, moof,
// mdat). Determinism is guaranteed: identical specs produce identical bytes.
func BuildFile(tracks []TrackSpec, segs []SegmentSpec) ([]byte, error) {
	if len(tracks) == 0 || len(tracks) > MaxWriteTracks {
		return nil, fmt.Errorf("%w: %d tracks", ErrLimit, len(tracks))
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("%w: no segments", ErrUnsupportedLayout)
	}
	decl := map[uint32]TrackSpec{}
	order := make([]uint32, 0, len(tracks))
	for _, t := range tracks {
		if t.ID == 0 || t.Timescale == 0 {
			return nil, fmt.Errorf("%w: track id/timescale", ErrIllegalLength)
		}
		if _, dup := decl[t.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate track %d", ErrTrackMismatch, t.ID)
		}
		if t.Handler != "vide" && t.Handler != "soun" {
			return nil, fmt.Errorf("%w: handler %q", ErrUnsupportedLayout, t.Handler)
		}
		decl[t.ID] = t
		order = append(order, t.ID)
	}
	var body bytes.Buffer
	bw := &boxWriter{w: &body}
	if err := bw.writeFtyp(); err != nil {
		return nil, err
	}
	if err := bw.writeMoov(tracks); err != nil {
		return nil, err
	}
	for i, seg := range segs {
		if seg.Sequence == 0 {
			return nil, fmt.Errorf("%w: segment %d sequence 0", ErrIllegalLength, i)
		}
		if err := bw.writeSegment(decl, order, seg); err != nil {
			return nil, err
		}
	}
	if body.Len() > MaxFileBytes {
		return nil, fmt.Errorf("%w: file %d bytes", ErrLimit, body.Len())
	}
	return body.Bytes(), nil
}

type boxWriter struct{ w *bytes.Buffer }

func (b *boxWriter) begin(typ string) int {
	start := b.w.Len()
	b.w.Write(make([]byte, 8))
	copy(b.w.Bytes()[start+4:], typ)
	return start
}

func (b *boxWriter) end(start int) {
	binary.BigEndian.PutUint32(b.w.Bytes()[start:], uint32(b.w.Len()-start))
}

func (b *boxWriter) nested(typ string, fn func() error) error {
	start := b.begin(typ)
	if err := fn(); err != nil {
		return err
	}
	b.end(start)
	return nil
}

func (b *boxWriter) fullBox(typ string, version uint8, flags uint32, fn func() error) error {
	start := b.begin(typ)
	b.w.Write([]byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)})
	if err := fn(); err != nil {
		return err
	}
	b.end(start)
	return nil
}

func (b *boxWriter) u8(v uint8)   { b.w.WriteByte(v) }
func (b *boxWriter) u16(v uint16) { b.w.Write([]byte{byte(v >> 8), byte(v)}) }
func (b *boxWriter) u32(v uint32) {
	b.w.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
func (b *boxWriter) u64(v uint64) {
	b.w.Write([]byte{byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32), byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
func (b *boxWriter) zero(n int)       { b.w.Write(make([]byte, n)) }
func (b *boxWriter) bytes(bts []byte) { b.w.Write(bts) }

func (b *boxWriter) matrix() {
	for i := 0; i < 9; i++ {
		switch i {
		case 0, 4:
			b.u32(0x00010000)
		case 8:
			b.u32(0x40000000)
		default:
			b.u32(0)
		}
	}
}

func (b *boxWriter) writeFtyp() error {
	return b.nested("ftyp", func() error {
		b.bytes([]byte("iso5"))
		b.u32(0)
		for _, brand := range []string{"iso5", "iso6", "mp41"} {
			b.bytes([]byte(brand))
		}
		return nil
	})
}

func (b *boxWriter) writeMoov(tracks []TrackSpec) error {
	return b.nested("moov", func() error {
		if err := b.fullBox("mvhd", 0, 0, func() error {
			b.u32(0)
			b.u32(0)
			b.u32(1000)
			b.u32(0)
			b.u32(0x00010000)
			b.u16(0x0100)
			b.zero(2)
			b.zero(8)
			b.matrix()
			b.zero(24)
			b.u32(uint32(len(tracks) + 1))
			return nil
		}); err != nil {
			return err
		}
		for _, t := range tracks {
			if err := b.writeTrak(t, t.ID); err != nil {
				return err
			}
		}
		return b.nested("mvex", func() error {
			for _, t := range tracks {
				if err := b.fullBox("trex", 0, 0, func() error {
					b.u32(t.ID)
					b.u32(1)
					b.u32(0)
					b.u32(0)
					b.u32(0)
					return nil
				}); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func (b *boxWriter) writeTrak(t TrackSpec, id uint32) error {
	return b.nested("trak", func() error {
		if err := b.fullBox("tkhd", 0, 0x3, func() error {
			b.u32(0)
			b.u32(0)
			b.u32(id)
			b.u32(0)
			b.u32(0)
			b.zero(8)
			b.u16(0)
			b.u16(0)
			if t.Handler == "soun" {
				b.u16(0x0100)
			} else {
				b.u16(0)
			}
			b.u16(0)
			b.matrix()
			b.u32(uint32(t.Width) << 16)
			b.u32(uint32(t.Height) << 16)
			return nil
		}); err != nil {
			return err
		}
		return b.nested("mdia", func() error {
			if err := b.fullBox("mdhd", 0, 0, func() error {
				b.u32(0)
				b.u32(0)
				b.u32(t.Timescale)
				b.u32(0)
				b.u16(0x55c4) // "und"
				b.u16(0)
				return nil
			}); err != nil {
				return err
			}
			if err := b.fullBox("hdlr", 0, 0, func() error {
				b.u32(0)
				b.bytes([]byte(t.Handler))
				b.zero(12)
				b.bytes([]byte("mediapipe"))
				b.u8(0)
				return nil
			}); err != nil {
				return err
			}
			return b.nested("minf", func() error {
				if t.Handler == "soun" {
					if err := b.fullBox("smhd", 0, 0, func() error {
						b.u16(0)
						b.u16(0)
						return nil
					}); err != nil {
						return err
					}
				} else {
					if err := b.fullBox("vmhd", 0, 1, func() error {
						b.u16(0)
						b.u16(0)
						b.u16(0)
						b.u16(0)
						return nil
					}); err != nil {
						return err
					}
				}
				if err := b.nested("dinf", func() error {
					return b.fullBox("dref", 0, 0, func() error {
						b.u32(1)
						return b.fullBox("url ", 0, 1, func() error { return nil })
					})
				}); err != nil {
					return err
				}
				return b.nested("stbl", func() error {
					if err := writeEmptyTables(b); err != nil {
						return err
					}
					return b.writeStsd(t)
				})
			})
		})
	})
}

// writeEmptyTables emits the empty sample tables of a fragmented track. The
// stsd entry is written separately because it is a container.
func writeEmptyTables(b *boxWriter) error {
	if err := b.fullBox("stts", 0, 0, func() error { b.u32(0); return nil }); err != nil {
		return err
	}
	if err := b.fullBox("stsc", 0, 0, func() error { b.u32(0); return nil }); err != nil {
		return err
	}
	if err := b.fullBox("stsz", 0, 0, func() error { b.u32(0); b.u32(0); return nil }); err != nil {
		return err
	}
	return b.fullBox("stco", 0, 0, func() error { b.u32(0); return nil })
}

func (b *boxWriter) writeStsd(t TrackSpec) error {
	return b.fullBox("stsd", 0, 0, func() error {
		b.u32(1)
		typ := "avc1"
		if t.Handler == "soun" {
			typ = "mp4a"
		}
		return b.nested(typ, func() error {
			b.zero(6)
			b.u16(1)
			if t.Handler == "soun" {
				// AudioSampleEntry: version(2) + reserved(6)
				b.u16(0)
				b.zero(6)
				b.u16(t.Channels)
				b.u16(t.SampleSize)
				b.u16(0)
				b.u16(0)
				b.u32(t.SampleRate << 16)
				return nil
			}
			b.u16(0)
			b.u16(0)
			b.zero(12)
			b.u16(t.Width)
			b.u16(t.Height)
			b.u32(0x00480000)
			b.u32(0x00480000)
			b.u32(0)
			b.u16(1)
			var name [32]byte
			name[0] = 4
			copy(name[1:], "mpip")
			b.bytes(name[:])
			b.u16(24)
			b.u16(0xFFFF)
			if err := b.nested("avcC", func() error {
				b.u8(1)
				b.u8(0x42)
				b.u8(0xC0)
				b.u8(0x1E)
				b.u8(0xFC | 3)
				b.u8(0xE0 | 1)
				b.u16(4)
				b.bytes([]byte{0x67, 0x42, 0xC0, 0x1E})
				b.u8(1)
				b.u16(2)
				b.bytes([]byte{0x68, 0xCE})
				return nil
			}); err != nil {
				return err
			}
			if t.Color != nil {
				return b.nested("colr", func() error {
					b.bytes([]byte("nclx"))
					b.u16(t.Color.Primaries)
					b.u16(t.Color.Transfer)
					b.u16(t.Color.Matrix)
					var fr byte
					if t.Color.FullRange {
						fr = 0x80
					}
					b.u8(fr)
					return nil
				})
			}
			return nil
		})
	})
}

func (b *boxWriter) writeSegment(decl map[uint32]TrackSpec, order []uint32, seg SegmentSpec) error {
	for _, e := range seg.Emsg {
		payload, err := meta.EncodeEmsg(e)
		if err != nil {
			return err
		}
		start := b.begin("emsg")
		b.bytes(payload)
		b.end(start)
	}
	counts := map[uint32]int{}
	for _, s := range seg.Samples {
		if _, ok := decl[s.TrackID]; !ok {
			return fmt.Errorf("%w: sample track %d undeclared", ErrTrackMismatch, s.TrackID)
		}
		if len(s.Payload) == 0 || len(s.Payload) > MaxSampleEntry {
			return fmt.Errorf("%w: sample payload %d", ErrIllegalLength, len(s.Payload))
		}
		counts[s.TrackID]++
		if counts[s.TrackID] > MaxWriteSamples {
			return fmt.Errorf("%w: samples in fragment", ErrLimit)
		}
	}
	if len(counts) == 0 {
		return fmt.Errorf("%w: empty segment", ErrUnsupportedLayout)
	}
	moofStart := b.w.Len()
	b.begin("moof")
	b.fullBox("mfhd", 0, 0, func() error { b.u32(seg.Sequence); return nil })
	trunOffsets := map[uint32]int{}
	for _, id := range order {
		n := counts[id]
		if n == 0 {
			continue
		}
		trafStart := b.begin("traf")
		if err := b.fullBox("tfhd", 0, 0x020000, func() error {
			b.u32(id)
			return nil
		}); err != nil {
			return err
		}
		if err := b.fullBox("tfdt", 0, 0, func() error {
			b.u32(uint32(seg.DTS[id]))
			return nil
		}); err != nil {
			return err
		}
		trunStart := b.begin("trun")
		trunOffsets[id] = trunStart + 16 // box(8) + version/flags(4) + count(4)
		flags := uint32(0x1 | 0x100 | 0x200 | 0x400 | 0x800)
		b.u8(0)
		b.w.Write([]byte{byte(flags >> 16), byte(flags >> 8), byte(flags)})
		b.u32(uint32(n))
		b.u32(0) // data offset, patched once layout is known
		for _, s := range seg.Samples {
			if s.TrackID != id {
				continue
			}
			b.u32(s.Duration)
			b.u32(uint32(len(s.Payload)))
			var sf uint32
			if !s.Sync {
				sf = NonSyncSampleFlag
			}
			b.u32(sf)
			b.u32(uint32(s.CTSDelta))
		}
		b.end(trunStart)
		b.end(trafStart)
	}
	b.end(moofStart)
	mdatStart := b.w.Len()
	b.begin("mdat")
	dataPos := b.w.Len()
	for _, id := range order {
		if counts[id] == 0 {
			continue
		}
		// Common case: every sample data chunk starts at the buffer position
		// recorded when patching. Interleaving is avoided so offsets are exact.
		binary.BigEndian.PutUint32(b.w.Bytes()[trunOffsets[id]:], uint32(dataPos-moofStart))
		for _, s := range seg.Samples {
			if s.TrackID != id {
				continue
			}
			b.bytes(s.Payload)
			dataPos += len(s.Payload)
		}
	}
	b.end(mdatStart)
	return nil
}

// WriteFileAtomic writes data to path via temp file + fsync + rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mp4tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
