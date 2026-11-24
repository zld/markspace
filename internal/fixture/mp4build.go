// Package fixture generates all sample data used by the kernel: MP4 files
// with edit lists and sparse sync samples, capture manifests, RTP packet logs
// and deliberately damaged variants. Nothing is loaded from outside the
// repository and every file is reproducible from a seed.
package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	mp4 "github.com/abema/go-mp4"
)

// MP4Track describes one track written into a generated MP4.
type MP4Track struct {
	ID        uint32
	Handler   [4]byte // "vide" or "soun"
	Timescale uint32
	// SampleDelta is the constant decode duration per sample, in timescale
	// units.
	SampleDelta uint32
	SampleCount uint32
	// SyncSamples are 1-based sample numbers marked as keyframes. Empty means
	// every sample is a sync sample.
	SyncSamples []uint32
	// Edits is the edit list. A nil slice writes no edts box.
	Edits []MP4Edit
	// Width/Height for video tracks, in 16.16 fixed point.
	Width, Height uint32
}

// MP4Edit is one elst entry in timescale units. MediaTime < 0 is an empty
// edit.
type MP4Edit struct {
	SegmentDuration uint32
	MediaTime       int32
}

// MP4Spec is the complete description of a generated MP4.
type MP4Spec struct {
	Timescale uint32
	Tracks    []MP4Track
	// AlignMdatTo, when positive, inserts a free box before mdat so the mdat
	// box header starts at exactly that byte offset. Damage fixtures rely on
	// a real box boundary landing on the injection point.
	AlignMdatTo int64
}

// SizeBytes returns the number of bytes one sample consumes in mdat.
const SizeBytes = 16

// BuildMP4 writes a deterministic MP4 file containing an ftyp, a moov with
// track headers, edit lists, sample tables and sync samples, and an mdat with
// correctly sized (opaque) sample data.
func BuildMP4(w io.WriteSeeker, spec MP4Spec) error {
	if len(spec.Tracks) == 0 {
		return fmt.Errorf("mp4 spec has no tracks")
	}
	if spec.Timescale == 0 {
		return fmt.Errorf("mp4 spec timescale must be positive")
	}
	mw := mp4.NewWriter(w)

	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	var movieDuration uint32
	for _, t := range spec.Tracks {
		d := t.SampleDelta * t.SampleCount
		if d > movieDuration {
			movieDuration = d
		}
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Mvhd{
		Timescale:   spec.Timescale,
		DurationV0:  movieDuration,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix(),
		NextTrackID: uint32(len(spec.Tracks) + 1),
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	for _, t := range spec.Tracks {
		if err := writeTrak(mw, t); err != nil {
			return fmt.Errorf("track %d: %w", t.ID, err)
		}
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	if spec.AlignMdatTo > 0 {
		cur, err := w.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		// mdat needs an 8-byte header; leave room for it.
		pad := spec.AlignMdatTo - cur
		if pad > 0 {
			// Write the padding without an end box: patching the free box
			// length in place would rewrite its header, and the writer keeps
			// its cursor after the payload.
			if _, err := mw.Write(padBytes(pad, mp4.StrToBoxType("free"))); err != nil {
				return err
			}
			if _, err := w.Seek(spec.AlignMdatTo, io.SeekStart); err != nil {
				return err
			}
		}
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	for _, t := range spec.Tracks {
		for i := uint32(0); i < t.SampleCount; i++ {
			var payload [SizeBytes]byte
			copy(payload[:], []byte(fmt.Sprintf("T%03dS%06d", t.ID, i)))
			if _, err := mw.Write(payload[:]); err != nil {
				return err
			}
		}
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	return nil
}

// padBytes builds a complete free box whose total size is n bytes.
func padBytes(n int64, typ mp4.BoxType) []byte {
	if n < 8 {
		return nil
	}
	out := make([]byte, n)
	binary.BigEndian.PutUint32(out[0:4], uint32(n))
	copy(out[4:8], typ[:])
	return out
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func writeTrak(mw *mp4.Writer, t MP4Track) error {
	dur := t.SampleDelta * t.SampleCount
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	vol := int16(0)
	if t.Handler == [4]byte{'s', 'o', 'u', 'n'} {
		vol = 0x0100
	}
	if _, err := mp4.Marshal(mw, &mp4.Tkhd{
		TrackID:    t.ID,
		DurationV0: dur,
		Volume:     vol,
		Matrix:     identityMatrix(),
		Width:      t.Width,
		Height:     t.Height,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	if len(t.Edits) > 0 {
		if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeEdts()}); err != nil {
			return err
		}
		if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeElst()}); err != nil {
			return err
		}
		entries := make([]mp4.ElstEntry, 0, len(t.Edits))
		for _, e := range t.Edits {
			entries = append(entries, mp4.ElstEntry{
				SegmentDurationV0: e.SegmentDuration,
				MediaTimeV0:       e.MediaTime,
			})
		}
		elst := &mp4.Elst{EntryCount: uint32(len(entries)), Entries: entries}
		elst.SetVersion(0)
		if _, err := mp4.Marshal(mw, elst, mp4.Context{}); err != nil {
			return err
		}
		if _, err := mw.EndBox(); err != nil {
			return err
		}
		if _, err := mw.EndBox(); err != nil {
			return err
		}
	}

	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Mdhd{
		Timescale:  t.Timescale,
		DurationV0: dur,
		Language:   [3]byte{'u', 'n', 'd'},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Hdlr{
		HandlerType: t.Handler,
		Name:        "mediasync",
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Stsd{EntryCount: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	if err := writeStts(mw, t); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if len(t.SyncSamples) > 0 {
		if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
			return err
		}
		stss := &mp4.Stss{EntryCount: uint32(len(t.SyncSamples)), SampleNumber: t.SyncSamples}
		if _, err := mp4.Marshal(mw, stss, mp4.Context{}); err != nil {
			return err
		}
		if _, err := mw.EndBox(); err != nil {
			return err
		}
	}
	if _, err := mw.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := mw.EndBox(); err != nil { // minf
		return err
	}
	if _, err := mw.EndBox(); err != nil { // mdia
		return err
	}
	if _, err := mw.EndBox(); err != nil { // trak
		return err
	}
	return nil
}

func writeStts(mw *mp4.Writer, t MP4Track) error {
	stts := &mp4.Stts{}
	if t.SampleCount > 0 {
		stts.EntryCount = 1
		stts.Entries = []mp4.SttsEntry{{SampleCount: t.SampleCount, SampleDelta: t.SampleDelta}}
	}
	_, err := mp4.Marshal(mw, stts, mp4.Context{})
	return err
}

// WriteFile builds an MP4 and atomically installs it at path.
func WriteFile(path string, spec MP4Spec) error {
	wsb := &writeSeekerBuffer{}
	if err := BuildMP4(wsb, spec); err != nil {
		return err
	}
	return os.WriteFile(path, wsb.buf.Bytes(), 0o644)
}

// writeSeekerBuffer adapts a bytes.Buffer to io.WriteSeeker. Box writing only
// uses current-position queries and seeks back to patch headers, so the
// adapter keeps an explicit cursor and never truncates on seek.
type writeSeekerBuffer struct {
	buf bytes.Buffer
	pos int64
}

func (b *writeSeekerBuffer) Write(p []byte) (int, error) {
	if b.pos > int64(b.buf.Len()) {
		b.buf.Write(make([]byte, b.pos-int64(b.buf.Len())))
	}
	data := b.buf.Bytes()
	if b.pos+int64(len(p)) > int64(len(data)) {
		grow := b.pos + int64(len(p)) - int64(len(data))
		data = append(data, make([]byte, grow)...)
		b.buf.Reset()
		b.buf.Write(data)
	}
	copy(b.buf.Bytes()[b.pos:], p)
	b.pos += int64(len(p))
	return len(p), nil
}

func (b *writeSeekerBuffer) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = b.pos + offset
	case io.SeekEnd:
		next = int64(b.buf.Len()) + offset
	default:
		return 0, fmt.Errorf("unsupported whence %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("negative seek position %d", next)
	}
	b.pos = next
	return next, nil
}

// CopyTruncated copies the first n bytes of src to dst, optionally injecting
// a damaged box length at the given offset. It returns the byte offset of the
// damage so tests can assert the diagnostic points at it.
func CopyTruncated(src, dst string, n int64, damageAt int64) (int64, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}
	if n <= 0 || n > int64(len(data)) {
		n = int64(len(data))
	}
	out := append([]byte(nil), data[:n]...)
	if damageAt >= 0 && damageAt+4 <= int64(len(out)) {
		// Overwrite the box length with a value that runs past the file.
		out[damageAt] = 0x7f
		out[damageAt+1] = 0xff
		out[damageAt+2] = 0xff
		out[damageAt+3] = 0xf0
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return 0, err
	}
	return int64(len(out)), nil
}
