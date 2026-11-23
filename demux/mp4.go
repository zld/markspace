// Package demux parses self-generated MP4/MOV fixtures into the timeline
// track graph, and contains the fixture writer used by tests and the demo
// command. No external media files are ever read.
package demux

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/abema/go-mp4"

	"mediadist/timeline"
)

// MaxInputBytes bounds any container the kernel will open: a hard guard
// against oversized samples and unrestricted allocation.
const MaxInputBytes int64 = 512 * 1024 * 1024 // 512 MiB

// MaxSamplesPerTrack bounds sample-table allocation from corrupt counts.
const MaxSamplesPerTrack = 5_000_000

// ErrInputLimit is the sentinel wrapped inside a LocError for oversized data.
var ErrInputLimit = errors.New("input exceeds configured safety limit")

// errIndexTruncated marks low-level array/box reads that ran past the payload,
// which always indicates a truncated or corrupt sample index.
var errIndexTruncated = errors.New("truncated sample index")

func isTruncationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "failed to read array completely") ||
		strings.Contains(msg, "unexpected EOF") ||
		strings.Contains(msg, "too large box size") ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF)
}

// limitedReadSeeker rejects reads and seeks past MaxInputBytes so a corrupt
// box header claiming a huge size cannot trigger unbounded allocation.
type limitedReadSeeker struct {
	rs    io.ReadSeeker
	limit int64
}

func (l *limitedReadSeeker) Read(p []byte) (int, error) {
	pos, err := l.rs.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if pos >= l.limit {
		return 0, io.EOF
	}
	if int64(len(p)) > l.limit-pos {
		p = p[:l.limit-pos]
	}
	n, err := l.rs.Read(p)
	if err == io.EOF && pos+int64(n) >= l.limit {
		err = nil
	}
	return n, err
}

func (l *limitedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	pos, err := l.rs.Seek(offset, whence)
	if err != nil {
		return 0, err
	}
	if pos > l.limit {
		return 0, fmt.Errorf("%w: seek to %d beyond limit %d", ErrInputLimit, pos, l.limit)
	}
	return pos, nil
}

// rawBox captures one visited box during the single structural walk.
type rawBox struct {
	info    *mp4.BoxInfo
	path    []mp4.BoxType
	box     mp4.IBox
	payload []byte // raw payload for unregistered custom boxes
	index   int    // ordinal among same-type sibling entries (stsc/stts)
}

// Parse reads an MP4/MOV container and returns the explicit track graph with
// every sample timestamp mapped onto the common clock using integer scaling.
// Faults are returned as timeline.LocError values carrying stage and track.
func Parse(r io.ReadSeeker, inputSize int64) (*timeline.Graph, error) {
	if inputSize <= 0 {
		return nil, timeline.NewLocError(timeline.CodeBadArgument, timeline.StageDemux, 0, -1, "size", nil)
	}
	if inputSize > MaxInputBytes {
		return nil, timeline.NewLocError(timeline.CodeUncheckedInput, timeline.StageDemux, 0, -1,
			"input-size", fmt.Errorf("%w: %d > %d", ErrInputLimit, inputSize, MaxInputBytes))
	}
	lr := &limitedReadSeeker{rs: r, limit: inputSize}
	raws, err := walk(lr)
	if err != nil {
		if errors.Is(err, errIndexTruncated) {
			return nil, timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux,
				0, -1, "sample-table", err)
		}
		return nil, timeline.NewLocError("CONTAINER_INVALID", timeline.StageDemux, 0, -1, "walk", err)
	}
	return buildGraph(raws)
}

// walk performs one ReadBoxStructure pass and materializes every supported box.
func walk(r io.ReadSeeker) ([]rawBox, error) {
	var out []rawBox
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		rb := rawBox{info: &h.BoxInfo, path: append([]mp4.BoxType(nil), h.Path...)}
		supported := h.BoxInfo.Type.IsSupported(h.BoxInfo.Context)
		if supported {
			box, _, err := h.ReadPayload()
			if err != nil {
				if isTruncationError(err) {
					return nil, fmt.Errorf("%w: %v", errIndexTruncated, err)
				}
				return nil, err
			}
			rb.box = box
			// Only registered boxes can be unmarshalled recursively. Custom
			// boxes (mdst) carry raw payload; SeekToEnd restores the position.
			if _, err := h.Expand(); err != nil {
				if isTruncationError(err) {
					return nil, fmt.Errorf("%w: %v", errIndexTruncated, err)
				}
				return nil, err
			}
		} else {
			var buf bytes.Buffer
			if _, err := h.ReadData(&buf); err != nil {
				return nil, err
			}
			if buf.Len() > 4096 {
				return nil, fmt.Errorf("%w: custom box payload %d > 4096", ErrInputLimit, buf.Len())
			}
			rb.payload = buf.Bytes()
		}
		out = append(out, rb)
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, err
	}
	return out, nil
}

// ParseBytes is the convenience entry point used by tests and the demo.
func ParseBytes(data []byte) (*timeline.Graph, error) {
	if int64(len(data)) > MaxInputBytes {
		return nil, timeline.NewLocError(timeline.CodeUncheckedInput, timeline.StageDemux, 0, -1,
			"input-size", fmt.Errorf("%w: %d bytes", ErrInputLimit, len(data)))
	}
	return Parse(bytes.NewReader(data), int64(len(data)))
}
