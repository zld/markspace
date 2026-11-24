// Package container scans MP4/MOV box structures defensively: a raw
// depth-limited pre-scan detects truncated boxes and unsupported depth
// before any third-party parser runs, then go-mp4 decodes the sample tables.
package container

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Parsing limits. The kernel analyzes generated fixtures and operator
// supplied local files, never unbounded streams: refuse pathological input
// early instead of allocating against it.
const (
	MaxInputBytes  = 512 << 20 // 512 MiB
	MaxBoxDepth    = 16
	MaxBoxCount    = 200_000
	SmallHeader    = 8
	LargeHeader    = 16
)

// containerBoxes are the box types whose payload is itself a box list.
// Everything else is treated as a leaf; leaves whose payloads are cut short
// are reported as truncated index boxes when they live under stbl.
var containerBoxes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "edts": true, "dinf": true, "udta": true,
	"mvex": true, "moof": true, "traf": true, "mfra": true, "meta": true,
}

// BoxRecord is one box observed during the raw pre-scan.
type BoxRecord struct {
	Type       string `json:"type"`
	Offset     int64  `json:"offset"`
	Size       uint64 `json:"size"`
	HeaderSize uint64 `json:"header_size"`
	Depth      int    `json:"depth"`
	// Truncated means the file ends before the declared payload does.
	Truncated bool `json:"truncated"`
}

// TrefEdge is one reference found inside a tref box: a four-char type and
// the referenced track ID(s). go-mp4 has no tref support, so the pre-scan
// parses these payloads itself.
type TrefEdge struct {
	FromTrackID uint32 `json:"from_track_id"`
	RefType     string `json:"ref_type"`
	ToTrackID   uint32 `json:"to_track_id"`
}

// PreScan is the output of the raw structural pass.
type PreScan struct {
	Boxes       []BoxRecord `json:"boxes"`
	Trefs       []TrefEdge  `json:"trefs"`
	Truncations []BoxRecord `json:"truncations"`
	FileSize    int64       `json:"file_size"`
}

// PreScanFile walks every box header with raw reads. It never trusts a
// declared size: a box reaching past EOF is recorded as truncated and
// traversal stops along that branch. Depth and box-count limits make the
// walk bounded on hostile input.
func PreScanFile(f *os.File) (*PreScan, error) {
	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat input: %w", err)
	}
	size := stat.Size()
	if size > MaxInputBytes {
		return nil, fmt.Errorf("input %d bytes exceeds limit %d", size, MaxInputBytes)
	}
	ps := &PreScan{FileSize: size}
	trackStack := []uint32{}
	if err := ps.walk(f, 0, uint64(size), 0, &trackStack); err != nil {
		return nil, err
	}
	return ps, nil
}

func (ps *PreScan) walk(f *os.File, start, end uint64, depth int, trackStack *[]uint32) error {
	if depth > MaxBoxDepth {
		return fmt.Errorf("box nesting exceeds depth limit %d", MaxBoxDepth)
	}
	pos := start
	for pos+SmallHeader <= end {
		if len(ps.Boxes) > MaxBoxCount {
			return fmt.Errorf("box count exceeds limit %d", MaxBoxCount)
		}
		hdr := make([]byte, SmallHeader)
		if _, err := f.ReadAt(hdr, int64(pos)); err != nil {
			return fmt.Errorf("read box header at %d: %w", pos, err)
		}
		declared := uint64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		headerSize := uint64(SmallHeader)
		switch declared {
		case 0:
			declared = end - pos // box extends to EOF
		case 1:
			ext := make([]byte, 8)
			if pos+LargeHeader > end {
				return ps.recordTrunc(f, typ, pos, headerSize, end-pos)
			}
			if _, err := f.ReadAt(ext, int64(pos+SmallHeader)); err != nil {
				return fmt.Errorf("read largesize: %w", err)
			}
			declared = binary.BigEndian.Uint64(ext)
			headerSize = LargeHeader
		}
		rec := BoxRecord{Type: typ, Offset: int64(pos), Size: declared, HeaderSize: headerSize, Depth: depth}
		if declared < headerSize || pos+declared > end {
			// Payload (or the rest of the header) is cut off.
			rec.Truncated = true
			ps.Boxes = append(ps.Boxes, rec)
			ps.Truncations = append(ps.Truncations, rec)
			return nil
		}
		ps.Boxes = append(ps.Boxes, rec)

		// Track ID bookkeeping for tref attribution.
		popped := false
		if typ == "trak" {
			*trackStack = append(*trackStack, 0)
		}
		if typ == "tkhd" {
			if id, ok := readTrackID(f, pos+headerSize, declared-headerSize); ok && len(*trackStack) > 0 {
				(*trackStack)[len(*trackStack)-1] = id
			}
		}
		if typ == "tref" {
			ps.readTref(f, pos+headerSize, declared-headerSize, trackStack)
		}

		if containerBoxes[typ] {
			if err := ps.walk(f, pos+headerSize, pos+declared, depth+1, trackStack); err != nil {
				return err
			}
		}
		if typ == "trak" {
			*trackStack = (*trackStack)[:len(*trackStack)-1]
			popped = true
		}
		_ = popped
		pos += declared
	}
	if pos != end {
		// Trailing bytes too short for another header: the last box was cut.
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (ps *PreScan) recordTrunc(f *os.File, typ string, pos, headerSize, available uint64) error {
	rec := BoxRecord{Type: typ, Offset: int64(pos), Size: headerSize + available, HeaderSize: headerSize, Depth: 0, Truncated: true}
	ps.Boxes = append(ps.Boxes, rec)
	ps.Truncations = append(ps.Truncations, rec)
	return nil
}

// readTrackID parses a full-box tkhd payload: version byte + 3 flag bytes,
// then v0/v1 timestamps, then the 32-bit track ID.
func readTrackID(f *os.File, payloadOff, payloadSize uint64) (uint32, bool) {
	if payloadSize < 4 {
		return 0, false
	}
	ver := make([]byte, 1)
	if _, err := f.ReadAt(ver, int64(payloadOff)); err != nil {
		return 0, false
	}
	idOff := payloadOff + 4
	if ver[0] == 1 {
		idOff += 16 // two 64-bit timestamps instead of two 32-bit
	} else {
		idOff += 8
	}
	buf := make([]byte, 4)
	if _, err := f.ReadAt(buf, int64(idOff)); err != nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(buf), true
}

// readTref parses the child reference boxes (each is a full box with a
// uint32 track-ID array payload).
func (ps *PreScan) readTref(f *os.File, off, size uint64, trackStack *[]uint32) {
	end := off + size
	var from uint32
	if len(*trackStack) > 0 {
		from = (*trackStack)[len(*trackStack)-1]
	}
	for off+SmallHeader <= end {
		hdr := make([]byte, SmallHeader)
		if _, err := f.ReadAt(hdr, int64(off)); err != nil {
			return
		}
		boxSize := uint64(binary.BigEndian.Uint32(hdr[0:4]))
		refType := string(hdr[4:8])
		if boxSize < SmallHeader || off+boxSize > end {
			return
		}
		payload := make([]byte, boxSize-SmallHeader)
		if _, err := f.ReadAt(payload, int64(off+SmallHeader)); err != nil {
			return
		}
		// Skip full-box version/flags (4 bytes), then read ID array.
		for i := 4; i+4 <= len(payload); i += 4 {
			ps.Trefs = append(ps.Trefs, TrefEdge{
				FromTrackID: from,
				RefType:     refType,
				ToTrackID:   binary.BigEndian.Uint32(payload[i : i+4]),
			})
		}
		off += boxSize
	}
}
