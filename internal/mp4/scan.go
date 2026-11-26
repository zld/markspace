// Package mp4 scans ISO-BMFF (MP4) containers and reports per-track structure
// and boundaries. Corrupt box length fields are reported with the exact byte
// offset of the offending field, and a bad track never pollutes other tracks.
package mp4

import (
	"encoding/binary"
	"io"
	"os"

	go_mp4 "github.com/abema/go-mp4"

	"regress/internal/media"
)

// Sample describes one media sample inside a track.
type Sample struct {
	Index     int
	Size      uint32
	Offset    uint64 // absolute file offset of the sample bytes
	Duration  uint32 // in track timescale units
}

// Boundary is a track timeline edge expressed both in track units and in the
// unified media time base.
type Boundary struct {
	Tick uint64    // track timescale units
	Time media.Rat // exact seconds
}

// TrackState is the scanned result of a single trak box.
type TrackState struct {
	TrackID         uint32
	Timescale       uint32
	DurationTicks   uint64
	Duration        media.Rat
	SampleCount     int
	Samples         []Sample
	Start           Boundary
	End             Boundary
	PayloadDigest   string
	ContainerDigest string
}

// FileState is the whole-file scan outcome. A per-track error is collected in
// TrackErrors while healthy tracks remain fully populated.
type FileState struct {
	Tracks       []TrackState
	TrackErrors  []*media.Error
	PayloadDigest  string
	ContainerDigest string
}

// ScanPath opens and scans an MP4 file under the 64 MiB input budget.
func ScanPath(path string) (*FileState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.InvalidInput("cannot open %s: %v", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, media.InvalidInput("cannot stat %s: %v", path, err)
	}
	if info.Size() > media.MaxInputBytes {
		return nil, &media.Error{
			Code: media.CodeOversize, Source: path,
			Detail: "mp4 input exceeds configured limit", Offset: info.Size(),
		}
	}
	return ScanReader(f, path)
}

// boxHeader is the raw eight-byte box header with its absolute offset.
type boxHeader struct {
	offset uint64
	size   uint64
	hsize  uint64
	typ    go_mp4.BoxType
}

// readHeader reads one box header exactly like the ISO-BMFF layout, so the
// returned offset points at the size field itself.
func readHeader(r io.ReadSeeker) (*boxHeader, error) {
	off, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	h := &boxHeader{
		offset: uint64(off),
		size:   uint64(binary.BigEndian.Uint32(hdr[0:4])),
		hsize:  8,
		typ:    go_mp4.BoxType{hdr[4], hdr[5], hdr[6], hdr[7]},
	}
	if h.size == 1 {
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		h.size = binary.BigEndian.Uint64(ext[:])
		h.hsize = 16
	}
	return h, nil
}

// ScanReader scans an MP4 stream. source names the asset in structured errors.
func ScanReader(r io.ReadSeeker, source string) (*FileState, error) {
	fileEnd, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	out := &FileState{}
	rootPay := media.NewDigester(media.KindPayload)
	rootCont := media.NewDigester(media.KindContainer)

	// Walk every top-level box. A corrupt length anywhere is reported with its
	// exact offset; scanning stops for that structure but prior tracks survive.
	var cursor uint64
	for cursor < uint64(fileEnd) {
		if _, err := r.Seek(int64(cursor), io.SeekStart); err != nil {
			return nil, err
		}
		h, err := readHeader(r)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil, media.Truncated(source, "trailing partial box header")
			}
			return nil, err
		}
		if h.size < h.hsize {
			return nil, media.CorruptBox(source, int64(h.offset),
				"declared box size smaller than its header")
		}
		if h.offset+h.size > uint64(fileEnd) {
			return nil, media.CorruptBox(source, int64(h.offset),
				"declared box size extends past end of file")
		}
		rootCont.WriteString(h.typ.String())
		switch h.typ {
		case go_mp4.BoxTypeMoov():
			if err := scanMoov(r, source, h, out, rootPay, rootCont); err != nil {
				return nil, err
			}
		case go_mp4.BoxTypeMdat():
			if err := digestBoxPayload(r, h, rootPay); err != nil {
				return nil, err
			}
		}
		cursor = h.offset + h.size
	}
	out.PayloadDigest = rootPay.Sum()
	out.ContainerDigest = rootCont.Sum()
	return out, nil
}

// scanMoov walks moov children, dispatching each trak independently so one
// corrupt track is isolated into TrackErrors.
func scanMoov(r io.ReadSeeker, source string, moov *boxHeader,
	out *FileState, rootPay, rootCont *media.Digester) error {
	children, err := childBoxes(r, moov)
	if err != nil {
		return err
	}
	for _, ch := range children {
		if ch.typ == go_mp4.BoxTypeTrak() {
			ts, terr := scanTrak(r, source, ch)
			if terr != nil {
				if me, ok := media.AsError(terr); ok {
					out.TrackErrors = append(out.TrackErrors, me)
					continue
				}
				return terr
			}
			out.Tracks = append(out.Tracks, *ts)
			rootPay.WriteString(ts.PayloadDigest)
			rootCont.WriteString(ts.ContainerDigest)
		}
	}
	return nil
}

// childBoxes enumerates immediate child boxes after validating each header.
func childBoxes(r io.ReadSeeker, parent *boxHeader) ([]*boxHeader, error) {
	var boxes []*boxHeader
	cursor := parent.offset + parent.hsize
	end := parent.offset + parent.size
	for cursor < end {
		if _, err := r.Seek(int64(cursor), io.SeekStart); err != nil {
			return nil, err
		}
		h, err := readHeader(r)
		if err != nil {
			return nil, media.Truncated(parent.typ.String(), "truncated child box")
		}
		if h.size < h.hsize || h.offset+h.size > end {
			return nil, media.CorruptBox(parent.typ.String(), int64(h.offset),
				"child box length exceeds its parent")
		}
		boxes = append(boxes, h)
		cursor = h.offset + h.size
	}
	return boxes, nil
}

// findChild returns the first child box of the given type, or nil.
func findChild(r io.ReadSeeker, parent *boxHeader, want go_mp4.BoxType) (*boxHeader, error) {
	kids, err := childBoxes(r, parent)
	if err != nil {
		return nil, err
	}
	for _, k := range kids {
		if k.typ == want {
			return k, nil
		}
	}
	return nil, nil
}

// unmarshalBox reads a known box payload into a go-mp4 structure.
func unmarshalBox(r io.ReadSeeker, h *boxHeader, dst go_mp4.IBox) error {
	if _, err := r.Seek(int64(h.offset+h.hsize), io.SeekStart); err != nil {
		return err
	}
	_, err := go_mp4.Unmarshal(r, h.size-h.hsize, dst, go_mp4.Context{})
	return err
}

// digestBoxPayload streams a box payload into the payload digest.
func digestBoxPayload(r io.ReadSeeker, h *boxHeader, d *media.Digester) error {
	if _, err := r.Seek(int64(h.offset+h.hsize), io.SeekStart); err != nil {
		return err
	}
	_, err := io.CopyN(d, r, int64(h.size-h.hsize))
	return err
}

// bytesAt returns n raw bytes at an absolute offset.
func bytesAt(r io.ReadSeeker, off uint64, n int) ([]byte, error) {
	if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
