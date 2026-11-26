// Package media inspects fMP4/CMAF segments: box structure validation,
// truncation detection, declared timing and payload extraction for the
// grayscale PSNR acceptance checks.
package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"continuity/internal/clock"
	"continuity/internal/manifest"
)

// SegmentInfo summarizes one inspected fMP4/CMAF segment.
type SegmentInfo struct {
	Path            string
	Size            int64
	HasSTyp         bool
	HasMoof         bool
	HasMdat         bool
	Timescale       uint32 // mdhd timescale when a moov is present, else 0
	DefaultDuration uint32 // tfhd default_sample_duration (CMAF fragments)
	BaseMediaDecode uint64 // tfdt baseMediaDecodeTime
	SampleCount     int
	SampleDeltas    []uint32 // per-sample durations from trun
	MdatPayload     []byte   // raw mdat content (bounded by 64 MiB)
}

// EffectiveTimescale returns the mdhd timescale, or 1 for the tfhd-based
// fragment convention where trun deltas are already in track tick units.
func (s *SegmentInfo) EffectiveTimescale() uint32 {
	if s.Timescale != 0 {
		return s.Timescale
	}
	return 1
}

// DurationRat converts the summed sample deltas and timescale into seconds.
func (s *SegmentInfo) DurationRat(declaredTimescale uint32) (clock.Rat, error) {
	var sum uint64
	if len(s.SampleDeltas) > 0 {
		for _, d := range s.SampleDeltas {
			sum += uint64(d)
		}
	} else if s.DefaultDuration != 0 {
		sum = uint64(s.DefaultDuration) * uint64(max2(s.SampleCount, 1))
	}
	if sum == 0 {
		return clock.Rat{}, clock.New(clock.KindCorruptMedia, "fmp4",
			"%s: zero total sample duration", s.Path)
	}
	ts := declaredTimescale
	if ts == 0 {
		ts = s.EffectiveTimescale()
	}
	if ts == 0 {
		return clock.Rat{}, clock.New(clock.KindCorruptMedia, "fmp4",
			"%s: no resolvable timescale", s.Path)
	}
	return clock.R(int64(sum), int64(ts))
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// boxCursor tracks the boxes observed during one read pass.
type boxCursor struct {
	info *SegmentInfo
}

// InspectSegment opens path, enforces the 64 MiB segment ceiling, walks the
// ISO-BMFF boxes via go-mp4 and validates the CMAF fragment contract.
func InspectSegment(path string) (*SegmentInfo, error) {
	info := &SegmentInfo{Path: path}
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.New(clock.KindIO, "fmp4", "open %s: %v", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, clock.New(clock.KindIO, "fmp4", "stat %s: %v", path, err)
	}
	info.Size = st.Size()
	if info.Size > manifest.MaxSegmentBytes {
		return nil, clock.New(clock.KindInvalidInput, "fmp4",
			"segment %s is %d bytes, exceeds 64 MiB limit", path, info.Size)
	}

	// Whole-file read is safe because of the 64 MiB ceiling and keeps mdat
	// payload extraction plus truncation checks simple and deterministic.
	raw, err := io.ReadAll(io.LimitReader(f, manifest.MaxSegmentBytes+1))
	if err != nil {
		return nil, clock.New(clock.KindIO, "fmp4", "read %s: %v", path, err)
	}
	if int64(len(raw)) > manifest.MaxSegmentBytes {
		return nil, clock.New(clock.KindInvalidInput, "fmp4",
			"segment %s exceeds 64 MiB", path)
	}
	if err := validateBoxes(raw, info); err != nil {
		return nil, err
	}
	if !info.HasMoof || !info.HasMdat {
		return nil, clock.New(clock.KindCorruptMedia, "fmp4",
			"%s: CMAF fragment must contain moof and mdat", path)
	}
	return info, nil
}

// validateBoxes performs a manual top-level box walk (so truncated mdat is
// detected precisely) and uses go-mp4 to decode the structural boxes.
func validateBoxes(raw []byte, info *SegmentInfo) error {
	// First: go-mp4 structural decode of moof children.
	if err := decodeWithMp4(raw, info); err != nil {
		return err
	}
	// Second: explicit top-level length walk for truncation detection.
	off := 0
	for off < len(raw) {
		if len(raw)-off < 8 {
			return clock.AtOffset(clock.New(clock.KindCorruptMedia, "fmp4",
				"%s: truncated box header at byte %d", info.Path, off), int64(off))
		}
		hdr := raw[off : off+8]
		size := int64(binary.BigEndian.Uint32(hdr[0:4]))
		boxType := string(hdr[4:8])
		headerLen := 8
		if size == 1 {
			if len(raw)-off < 16 {
				return clock.AtOffset(clock.New(clock.KindCorruptMedia, "fmp4",
					"%s: truncated largesize box", info.Path), int64(off))
			}
			size = int64(binary.BigEndian.Uint64(raw[off+8 : off+16]))
			headerLen = 16
		}
		if size == 0 {
			// Box extends to EOF; that is only valid for the final box.
			size = int64(len(raw) - off)
		}
		if size < int64(headerLen) {
			return clock.AtOffset(clock.New(clock.KindCorruptMedia, "fmp4",
				"%s: box %s declares invalid size %d", info.Path, boxType, size),
				int64(off))
		}
		if int64(off)+size > int64(len(raw)) {
			return clock.AtOffset(clock.New(clock.KindCorruptMedia, "fmp4",
				"%s: truncated %s box: declared %d bytes, %d available",
				info.Path, boxType, size, len(raw)-off), int64(off))
		}
		switch boxType {
		case "styp":
			info.HasSTyp = true
		case "moof":
			info.HasMoof = true
		case "mdat":
			info.HasMdat = true
			info.MdatPayload = append(info.MdatPayload,
				raw[off+headerLen:int64(off)+size]...)
		}
		off += int(size)
	}
	return nil
}

func decodeWithMp4(raw []byte, info *SegmentInfo) error {
	r := bytes.NewReader(raw)
	cur := &boxCursor{info: info}
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (any, error) {
		switch h.BoxInfo.Type.String() {
		case "moof", "traf", "mvex", "moov", "trak", "mdia", "minf", "stbl":
			// Container boxes must be expanded for the callback to descend.
			return h.Expand()
		case "tfhd":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, cur.err("tfhd decode", err)
			}
			if tf, ok := box.(*mp4.Tfhd); ok {
				info.DefaultDuration = tf.DefaultSampleDuration
			}
		case "tfdt":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, cur.err("tfdt decode", err)
			}
			if tf, ok := box.(*mp4.Tfdt); ok {
				// BaseMediaDecodeTime is stored in the version-specific field.
				info.BaseMediaDecode = tf.BaseMediaDecodeTimeV1
				if tf.Version == 0 {
					info.BaseMediaDecode = uint64(tf.BaseMediaDecodeTimeV0)
				}
			}
		case "trun":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, cur.err("trun decode", err)
			}
			if tr, ok := box.(*mp4.Trun); ok {
				info.SampleCount = int(tr.SampleCount)
				for _, e := range tr.Entries {
					info.SampleDeltas = append(info.SampleDeltas, e.SampleDuration)
				}
			}
		case "mdhd":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, cur.err("mdhd decode", err)
			}
			if md, ok := box.(*mp4.Mdhd); ok {
				info.Timescale = md.Timescale
			}
		}
		return nil, nil
	})
	if err != nil {
		return clock.New(clock.KindCorruptMedia, "fmp4",
			"%s: go-mp4 structure error: %v", info.Path, err)
	}
	return nil
}

func (b *boxCursor) err(op string, wrap error) error {
	return clock.New(clock.KindCorruptMedia, "fmp4",
		"%s: %s: %v", b.info.Path, op, wrap)
}

// String gives a compact debug representation.
func (s *SegmentInfo) String() string {
	return fmt.Sprintf("SegmentInfo(path=%s size=%d moof=%v mdat=%v samples=%d)",
		s.Path, s.Size, s.HasMoof, s.HasMdat, s.SampleCount)
}
