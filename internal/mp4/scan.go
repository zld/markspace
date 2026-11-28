// Package mp4 scans generated MP4 byte streams for track time bases, sample
// offsets and key frame positions. A truncated top-level box is located at its
// start offset and recorded as an error without aborting the scan of any
// recoverable boxes that follow.
package mp4

import (
	"bytes"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"medcheck/internal/model"
)

// BoxRecord is one top-level box with its exact byte span.
type BoxRecord struct {
	Type   string
	Offset int64
	Size   int64
	Header int64
}

// SampleInfo is one sample mapped to a byte offset and event time.
type SampleInfo struct {
	Index      uint32 // 0-based
	Offset     int64  // byte offset inside the input stream
	Size       uint32
	Ticks      int64
	IsKeyFrame bool
}

// TrackScan is one track's time base and sample table.
type TrackScan struct {
	TrackID   uint32
	Timescale model.Rational
	Samples   []SampleInfo
	KeyFrames []uint32 // 0-based sample indexes
}

// Scan is the full result of scanning one stream.
type Scan struct {
	Stream string
	Boxes  []BoxRecord
	Tracks []*TrackScan
	Errors []*model.CodeError
}

// ScanStream walks top-level boxes. Recoverable truncation errors are attached
// to Scan.Errors; the returned error is non-nil only for non-recoverable I/O.
func ScanStream(r io.ReadSeeker, stream string) (*Scan, error) {
	scan := &Scan{Stream: stream}
	streamLen, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var pos int64
	for {
		bi, err := gomp4.ReadBoxInfo(r)
		if err == io.EOF {
			// A clean EOF at an exact box boundary means the stream finished.
			// If we had begun reading a partial header, ReadBoxInfo reports
			// io.EOF while the cursor is not aligned to pos; detect that.
			cur, _ := r.Seek(0, io.SeekCurrent)
			if cur != pos {
				scan.Errors = append(scan.Errors, &model.CodeError{
					Kind:     model.KindTruncatedBox,
					Offset:   model.RawOffset{Stream: stream, Start: pos, End: cur},
					FirstPos: pos,
					Message:  "truncated box header",
				})
			}
			return scan, nil
		}
		if err == io.ErrUnexpectedEOF || err == io.ErrShortBuffer {
			scan.Errors = append(scan.Errors, &model.CodeError{
				Kind:     model.KindTruncatedBox,
				Offset:   model.RawOffset{Stream: stream, Start: pos, End: pos},
				FirstPos: pos,
				Message:  "truncated box header",
			})
			return scan, nil
		}
		if err != nil {
			return scan, err
		}
		rec := BoxRecord{
			Type:   bi.Type.String(),
			Offset: int64(bi.Offset),
			Size:   int64(bi.Size),
			Header: int64(bi.HeaderSize),
		}
		end := int64(bi.Offset) + int64(bi.Size)
		// A declared size running past the stream length is a truncated box;
		// we keep its start offset and stop scanning this stream. A box that
		// legitimately extends to EOF is left intact.
		if !bi.ExtendToEOF && end > streamLen {
			scan.Boxes = append(scan.Boxes, rec)
			scan.Errors = append(scan.Errors, &model.CodeError{
				Kind:     model.KindTruncatedBox,
				Offset:   model.RawOffset{Stream: stream, Start: int64(bi.Offset), End: end},
				FirstPos: int64(bi.Offset),
				Message:  "box size exceeds stream length",
			})
			return scan, nil
		}
		if bi.ExtendToEOF {
			end = streamLen
		}
		if _, serr := r.Seek(end, io.SeekStart); serr != nil {
			return scan, serr
		}
		scan.Boxes = append(scan.Boxes, rec)
		pos = end
	}
}

// ParseMoov decodes track time bases, sample offsets and key frames from the
// moov box. It must be called with the seeker positioned at the stream start;
// the moov location is discovered via ReadBoxStructure.
func (s *Scan) ParseMoov(r io.ReadSeeker) error {
	tracks, err := readTracks(r, s.Stream)
	if err != nil {
		return err
	}
	s.Tracks = tracks
	return nil
}

// ReadAll is the convenience entry point used by the pipeline.
func ReadAll(r io.ReadSeeker, stream string) (*Scan, error) {
	scan, err := ScanStream(r, stream)
	if err != nil {
		return nil, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := scan.ParseMoov(r); err != nil {
		// A broken moov still leaves the top-level box list and errors valid.
		if ce := model.AsCodeError(err); ce != nil {
			scan.Errors = append(scan.Errors, ce)
			return scan, nil
		}
		return scan, err
	}
	return scan, nil
}

// ParseBytes runs the full scan over an in-memory stream.
func ParseBytes(data []byte, stream string) (*Scan, error) {
	return ReadAll(bytes.NewReader(data), stream)
}
