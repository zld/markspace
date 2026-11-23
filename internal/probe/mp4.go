package probe

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"
	"github.com/sunfish-shogi/bufseekio"
	"mediapack/internal/model"
)

// mp4Scan walks the box tree with bounded depth/count and collects payloads
// needed for track metadata. Truncated and illegal-length boxes are reported
// with the exact offset of the offending header.
type mp4Scan struct {
	path   string
	r      io.ReaderAt
	size   int64
	lim    Limits
	errs   model.Errors
	boxes  int
	depth  int
	tracks []*mp4TrackState
	cur    *mp4TrackState
	brand  string
}

type mp4TrackState struct {
	meta TrackMeta
}

// ScanMP4 scans one ISO BMFF/MP4 input and returns its metadata.
func ScanMP4(path string, lim Limits) (*FileMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > lim.MaxInputBytes {
		var es model.Errors
		es.Newf(model.StageProbe, model.CodeLimitExceeded, path, 0,
			ErrorRange(0, st.Size()), "input %d exceeds limit %d", st.Size(), lim.MaxInputBytes)
		return nil, es.Err()
	}
	sum, err := sha256File(f, st.Size())
	if err != nil {
		return nil, err
	}
	s := &mp4Scan{path: path, r: f, size: st.Size(), lim: lim}

	// go-mp4 validates the framing layer. A buffered seeker keeps reads
	// bounded; the underlying *os.File still serves random payload reads.
	rs := bufseekio.NewReadSeeker(f, 64*1024, 4)
	_, gerr := gomp4.ReadBoxStructure(rs, func(h *gomp4.ReadHandle) (interface{}, error) {
		bi := h.BoxInfo
		s.depth = len(h.Path)
		if s.boxes++; s.boxes > lim.MaxBoxes {
			s.errs.Newf(model.StageProbe, model.CodeLimitExceeded, path, 0,
				ErrorRange(int64(bi.Offset), int64(bi.HeaderSize)),
				"box count exceeds %d", lim.MaxBoxes)
			return nil, io.EOF
		}
		if s.depth > lim.MaxBoxDepth {
			s.errs.Newf(model.StageProbe, model.CodeLimitExceeded, path, 0,
				ErrorRange(int64(bi.Offset), int64(bi.HeaderSize)),
				"box depth exceeds %d", lim.MaxBoxDepth)
			return nil, io.EOF
		}
		if bi.Offset+bi.Size > uint64(s.size) {
			s.errs.Newf(model.StageProbe, model.CodeTruncatedBox, path, 0,
				ErrorRange(int64(bi.Offset), int64(bi.HeaderSize)),
				"box '%s' size=%d extends past end of file (size=%d)",
				bi.Type, bi.Size, s.size)
			return nil, io.EOF
		}
		if bi.Size < bi.HeaderSize || bi.Size < 8 {
			s.errs.Newf(model.StageProbe, model.CodeIllegalLength, path, 0,
				ErrorRange(int64(bi.Offset), int64(bi.HeaderSize)),
				"box '%s' declares illegal size %d", bi.Type, bi.Size)
			return nil, io.EOF
		}
		container, err := s.handleBox(bi)
		if err != nil {
			return nil, err
		}
		if !container {
			return nil, nil // skip children
		}
		return h.Expand()
	})
	if gerr != nil && s.errs.Len() == 0 {
		// Truncated stream caught at the framing layer: attribute to the
		// highest byte the reader reached.
		off := st.Size()
		s.errs.Newf(model.StageProbe, model.CodeTruncatedBox, path, 0,
			ErrorRange(off, 8), "truncated box stream: %v", gerr)
	}
	if s.errs.Len() > 0 {
		return nil, s.errs.Err()
	}
	out := &FileMeta{
		Path: path, Format: "mp4", Size: st.Size(), SHA256: sum,
		Brand: s.brand, BoxCount: s.boxes, ScanVer: ScanVersion,
	}
	for _, ts := range s.tracks {
		out.Tracks = append(out.Tracks, ts.meta)
	}
	if len(out.Tracks) == 0 {
		s.errs.Newf(model.StageProbe, model.CodeMissingTrack, path, 0,
			ErrorRange(0, st.Size()), "no playable tracks found")
		return nil, s.errs.Err()
	}
	return out, nil
}

var mp4Containers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"dinf": true, "stbl": true, "edts": true, "udta": true,
}

// handleBox parses one box payload and reports whether children should expand.
func (s *mp4Scan) handleBox(bi gomp4.BoxInfo) (bool, error) {
	name := bi.Type.String()
	dataOff := int64(bi.Offset + bi.HeaderSize)
	dataLen := int64(bi.Size - bi.HeaderSize)
	switch name {
	case "ftyp":
		if b := readN(s.r, dataOff, min64(dataLen, 4)); b != nil {
			s.brand = string(bytes.TrimRight(b, "\x00"))
		}
	case "trak":
		s.cur = &mp4TrackState{meta: TrackMeta{Kind: KindData}}
		s.tracks = append(s.tracks, s.cur)
		if len(s.tracks) > s.lim.MaxTracks {
			s.errs.Newf(model.StageProbe, model.CodeLimitExceeded, s.path, 0,
				ErrorRange(int64(bi.Offset), int64(bi.Size)),
				"track count exceeds %d", s.lim.MaxTracks)
			return false, io.EOF
		}
	case "tkhd":
		if s.cur != nil {
			s.cur.meta.ID = trackIDAt(s.r, dataOff, dataLen)
			if rot := rotationAt(s.r, dataOff, dataLen, int64(bi.Offset)); rot != nil {
				s.cur.meta.Rotation = rot
			}
		}
	case "hdlr":
		if s.cur != nil {
			s.cur.meta.Handler = handlerTypeAt(s.r, dataOff, dataLen)
			s.cur.meta.Kind = kindFromHandler(s.cur.meta.Handler)
		}
	case "mdhd":
		if s.cur != nil {
			ts, first, dur := mdhdAt(s.r, dataOff, dataLen)
			s.cur.meta.TimeScale = ts
			s.cur.meta.FirstSampleTS = first
			s.cur.meta.DurationTS = dur
		}
	case "clap":
		if s.cur != nil && dataLen >= 16 {
			if b := readN(s.r, dataOff, 16); b != nil {
				s.cur.meta.CleanAperture = &SourcedValue{
					Source: "clap", Offset: int64(bi.Offset),
					Raw: fmt.Sprintf("widthN=%d widthD=%d heightN=%d heightD=%d",
						binary.BigEndian.Uint32(b[0:]), binary.BigEndian.Uint32(b[4:]),
						binary.BigEndian.Uint32(b[8:]), binary.BigEndian.Uint32(b[12:])),
					Value: "clean-aperture-declared",
				}
			}
		}
	case "colr":
		if s.cur != nil {
			s.cur.meta.Color = colrAt(s.r, dataOff, dataLen, int64(bi.Offset))
		}
	case "stsz":
		if s.cur != nil {
			if n := stszCountAt(s.r, dataOff, dataLen); n >= 0 {
				s.cur.meta.SampleCount = int64(n)
			}
		}
	}
	return mp4Containers[name], nil
}

func readN(r io.ReaderAt, off int64, n int64) []byte {
	if off < 0 || n <= 0 || n > 1<<24 {
		return nil
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, off); err != nil {
		return nil
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func kindFromHandler(h string) Kind {
	switch h {
	case "vide":
		return KindVideo
	case "soun":
		return KindAudio
	default:
		return KindData
	}
}
