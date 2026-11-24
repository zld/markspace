package scan

import (
	"bytes"
	"encoding/json"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"archive/internal/model"
)

// MetaUUID identifies our trak-level metadata box carrying crop and colour
// descriptions that may conflict with each other. Values are preserved
// verbatim; the scanner only flags the conflict.
var MetaUUID = [16]byte{0xA1, 0xC4, 0x7E, 0x02, 0x5B, 0x91, 0x4E, 0x33,
	0x8F, 0x6D, 0x10, 0x77, 0x0B, 0xB5, 0xD0, 0x01}

// MetaExtra is the payload of the MetaUUID box (JSON, fixed field order).
type MetaExtra struct {
	Crop     *Crop      `json:"crop,omitempty"`
	Color    *ColorDesc `json:"color,omitempty"`
	ColorAlt *ColorDesc `json:"colorAlt,omitempty"`
}

// maxBoxPayload caps metadata reads so a corrupt size field cannot make the
// scanner allocate unbounded memory.
const maxBoxPayload = 16 << 20

type mp4Scanner struct {
	f    *os.File
	size int64
	res  *Result
	trak *trakBuild
}

type trakBuild struct {
	meta   TrackMeta
	stts   []mp4.SttsEntry
	stsc   []mp4.StscEntry
	sizes  []uint32
	chunks []uint64
}

// ScanMP4 parses an MP4/ISOBMFF file: box boundaries, track metadata and
// sample tables, then validates every sample's essence packet so corruption
// is localised to one packet even when the container index looks fine.
func ScanMP4(path string) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	s := &mp4Scanner{f: f, size: st.Size(), res: &Result{Format: "mp4", Size: st.Size()}}
	s.walk(0, s.size, nil)
	if s.trak != nil { // unterminated trak (truncated moov)
		s.finishTrak()
	}
	for i := range s.res.Tracks {
		if d := s.res.Tracks[i].DurationNs(); d > s.res.DurationNs {
			s.res.DurationNs = d
		}
	}
	return s.res, nil
}

func (s *mp4Scanner) err(code model.ErrCode, off int64, track int, detail string) {
	s.res.Errors = append(s.res.Errors, model.ScanError{Code: code, Offset: off, Track: track, Detail: detail})
}

// walk reads sibling boxes in [start,end). It never returns an error to its
// caller: every defect is recorded with its offset and the walk either
// resynchronises or stops the current level.
func (s *mp4Scanner) walk(start, end int64, path []string) {
	off := start
	for off+8 <= end {
		if _, err := s.f.Seek(off, io.SeekStart); err != nil {
			return
		}
		bi, err := mp4.ReadBoxInfo(s.f)
		if err != nil {
			s.err(model.ErrTruncated, off, curTrack(s), "box header cut off: "+err.Error())
			return
		}
		boxEnd := int64(bi.Offset + bi.Size)
		if bi.Size < bi.HeaderSize && !bi.ExtendToEOF {
			s.err(model.ErrCorruptBox, off, curTrack(s), "invalid box size")
			next := s.resync(off+1, end)
			if next < 0 {
				return
			}
			off = next
			continue
		}
		if boxEnd > s.size && !bi.ExtendToEOF {
			s.err(model.ErrTruncated, off, curTrack(s), "box extends past end of file")
			return
		}
		if boxEnd > end {
			boxEnd = end // nested box clipped by parent; tolerate and continue
		}
		s.handle(bi, boxEnd, path)
		off = boxEnd
	}
	if off != end && end-off > 0 && len(path) == 0 {
		s.err(model.ErrTruncated, off, curTrack(s), "trailing bytes after last top-level box")
	}
}

func (s *mp4Scanner) handle(bi *mp4.BoxInfo, boxEnd int64, path []string) {
	typ := bi.Type.String()
	switch typ {
	case "moov", "mdia", "minf", "stbl":
		s.walk(int64(bi.Offset+bi.HeaderSize), boxEnd, append(path, typ))
	case "trak":
		s.trak = &trakBuild{}
		s.walk(int64(bi.Offset+bi.HeaderSize), boxEnd, append(path, typ))
		s.finishTrak()
	case "tkhd":
		var b mp4.Tkhd
		if s.unmarshal(bi, &b) && s.trak != nil {
			s.trak.meta.ID = int(b.TrackID)
			s.trak.meta.Rotation, s.trak.meta.HasRotation = rotationFromMatrix(b.Matrix)
		}
	case "mdhd":
		var b mp4.Mdhd
		if s.unmarshal(bi, &b) && s.trak != nil {
			s.trak.meta.Timescale = int64(b.Timescale)
			s.trak.meta.DurationUnits = int64(b.GetDuration())
		}
	case "hdlr":
		var b mp4.Hdlr
		if s.unmarshal(bi, &b) && s.trak != nil {
			switch string(b.HandlerType[:]) {
			case "vide":
				s.trak.meta.Type = "video"
			case "soun":
				s.trak.meta.Type = "audio"
			}
		}
	case "stts":
		var b mp4.Stts
		if s.unmarshal(bi, &b) {
			s.trak.stts = b.Entries
		}
	case "stsc":
		var b mp4.Stsc
		if s.unmarshal(bi, &b) {
			s.trak.stsc = b.Entries
		}
	case "stsz":
		var b mp4.Stsz
		if s.unmarshal(bi, &b) {
			s.trak.sizes = b.EntrySize
			if b.SampleSize != 0 { // uniform size
				s.trak.sizes = make([]uint32, b.SampleCount)
				for i := range s.trak.sizes {
					s.trak.sizes[i] = b.SampleSize
				}
			}
		}
	case "stco":
		var b mp4.Stco
		if s.unmarshal(bi, &b) {
			for _, o := range b.ChunkOffset {
				s.trak.chunks = append(s.trak.chunks, uint64(o))
			}
		}
	case "uuid":
		s.handleUUID(bi)
	}
}

func (s *mp4Scanner) unmarshal(bi *mp4.BoxInfo, dst mp4.IBox) bool {
	if bi.Size > maxBoxPayload {
		s.err(model.ErrCorruptBox, int64(bi.Offset), curTrack(s), "metadata box too large")
		return false
	}
	if _, err := s.f.Seek(int64(bi.Offset), io.SeekStart); err != nil {
		return false
	}
	if _, err := bi.SeekToPayload(s.f); err != nil {
		return false
	}
	if _, err := mp4.Unmarshal(s.f, bi.Size-bi.HeaderSize, dst, mp4.Context{}); err != nil {
		s.err(model.ErrCorruptBox, int64(bi.Offset), curTrack(s), "cannot parse "+bi.Type.String())
		return false
	}
	return true
}

func (s *mp4Scanner) handleUUID(bi *mp4.BoxInfo) {
	payload := int64(bi.Size - bi.HeaderSize)
	if payload < 16 || payload > maxBoxPayload {
		return
	}
	buf := make([]byte, payload)
	if _, err := s.f.ReadAt(buf, int64(bi.Offset+bi.HeaderSize)); err != nil {
		return
	}
	if !bytes.Equal(buf[:16], MetaUUID[:]) || s.trak == nil {
		return
	}
	var extra MetaExtra
	if err := json.Unmarshal(buf[16:], &extra); err != nil {
		s.err(model.ErrCorruptBox, int64(bi.Offset), curTrack(s), "bad trak metadata")
		return
	}
	s.trak.meta.Crop = extra.Crop
	s.trak.meta.Color = extra.Color
	s.trak.meta.ColorAlt = extra.ColorAlt
	if extra.Color != nil && extra.ColorAlt != nil && *extra.Color != *extra.ColorAlt {
		s.trak.meta.ColorConflict = true
	}
}

func curTrack(s *mp4Scanner) int {
	if s.trak == nil {
		return -1
	}
	return s.trak.meta.ID
}
