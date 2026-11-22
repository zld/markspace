package analyze

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediamux/media"
)

// readBoxAt parses one box header inside [off,end) with go-mp4 and rejects
// lengths that would run past the trusted end of the container.
func readBoxAt(data []byte, off, end int64) (*mp4.BoxInfo, error) {
	if off+8 > end {
		return nil, fmt.Errorf("short box header at %d", off)
	}
	r := &sliceReadSeeker{data: data, off: off}
	bi, err := mp4.ReadBoxInfo(r)
	if err != nil {
		return nil, fmt.Errorf("invalid box header at %d: %w", off, err)
	}
	if bi.Size < bi.HeaderSize {
		return nil, fmt.Errorf("box %s size %d below header %d", bi.Type, bi.Size, bi.HeaderSize)
	}
	if int64(bi.Size) > end-off {
		return nil, fmt.Errorf("box %s length %d overruns container end %d at %d", bi.Type, bi.Size, end-off, off)
	}
	return bi, nil
}

// parseLeaf unmarshals a box payload with go-mp4 and folds its facts into the
// running track state. Parse failures are ignored here: they surface as a
// missing index, which finishSegment marks as damage.
// parseLeaf unmarshals a box payload with go-mp4 and folds its facts into the
// running track state. Parse failures are ignored here: they surface as a
// missing index, which finishSegment marks as damage.
func parseLeaf(data []byte, bi *mp4.BoxInfo, track *TrackInfo, res *SegmentResult) {
	if track == nil {
		return
	}
	payload := data[bi.Offset+bi.HeaderSize : bi.Offset+bi.Size]
	r := &sliceReadSeeker{data: payload}
	switch bi.Type.String() {
	case "mdhd":
		var b mp4.Mdhd
		if _, err := mp4.Unmarshal(r, uint64(len(payload)), &b, mp4.Context{}); err == nil {
			track.Timescale = int64(b.Timescale)
			track.Duration = int64(b.GetDuration())
		}
	case "tkhd":
		var b mp4.Tkhd
		if _, err := mp4.Unmarshal(r, uint64(len(payload)), &b, mp4.Context{}); err == nil {
			track.ID = b.TrackID
		}
	case "hdlr":
		var b mp4.Hdlr
		if _, err := mp4.Unmarshal(r, uint64(len(payload)), &b, mp4.Context{}); err == nil {
			track.Handler = string(b.HandlerType[:])
		}
	case "stts":
		var b mp4.Stts
		if _, err := mp4.Unmarshal(r, uint64(len(payload)), &b, mp4.Context{}); err == nil {
			idx := len(res.Samples)
			var t uint64
			for _, e := range b.Entries {
				if e.SampleCount > uint32(res.Bytes+1) {
					break
				}
				for n := uint32(0); n < e.SampleCount; n++ {
					res.Samples = append(res.Samples, SampleRecord{
						TrackID: track.ID, Index: idx, Duration: e.SampleDelta, DecodeTime: int64(t),
					})
					t += uint64(e.SampleDelta)
					idx++
				}
			}
		}
	case "stsz":
		var b mp4.Stsz
		if _, err := mp4.Unmarshal(r, uint64(len(payload)), &b, mp4.Context{}); err == nil {
			for i := range res.Samples {
				if b.SampleSize != 0 {
					res.Samples[i].Size = b.SampleSize
				} else if int(i) < len(b.EntrySize) {
					res.Samples[i].Size = b.EntrySize[i]
				}
			}
		}
	case "stss":
		var b mp4.Stss
		if _, err := mp4.Unmarshal(r, uint64(len(payload)), &b, mp4.Context{}); err == nil {
			for _, n := range b.SampleNumber {
				if n >= 1 && int(n) <= len(res.Samples) {
					res.Samples[n-1].Keyframe = true
					res.Keyframes = append(res.Keyframes, int64(n))
				}
			}
		}
	case "trun":
		var b mp4.Trun
		if _, err := mp4.Unmarshal(r, uint64(len(payload)), &b, mp4.Context{}); err == nil {
			for _, e := range b.Entries {
				res.Keyframes = append(res.Keyframes, int64(len(res.Samples)))
				res.Samples = append(res.Samples, SampleRecord{
					TrackID: track.ID, Index: len(res.Samples), Size: e.SampleSize,
					Duration: e.SampleDuration, Keyframe: true,
				})
			}
		}
	case "tfhd":
		var b mp4.Tfhd
		if _, err := mp4.Unmarshal(r, uint64(len(payload)), &b, mp4.Context{}); err == nil {
			track.ID = b.TrackID
		}
	}
}

// recoverBoundary finds the next plausible box header at or after off. Boxes
// are 4-byte aligned in ISO-BMFF, so only aligned offsets are considered.
func recoverBoundary(data []byte, off, end int64) (int64, bool) {
	p := (off + 3) &^ 3
	for ; p+8 <= end; p += 4 {
		bi, err := readBoxAt(data, p, end)
		if err != nil {
			continue
		}
		if bi.Size >= 8 && isPrintableType(bi.Type) {
			return p, true
		}
	}
	return 0, false
}

func isPrintableType(t mp4.BoxType) bool {
	for _, c := range t {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// keyframeGapsMS computes inter-keyframe spacing on the unified timeline.
func keyframeGapsMS(keys []int64, samples []SampleRecord, ts media.Timescale) []int64 {
	if len(keys) < 2 || ts <= 0 {
		return nil
	}
	var gaps []int64
	for i := 1; i < len(keys); i++ {
		var ticks int64
		for j := keys[i-1]; j < keys[i] && int(j) < len(samples); j++ {
			ticks += int64(samples[j].Duration)
		}
		gaps = append(gaps, scaleTicks(ticks, ts, media.TimescaleMS))
	}
	return gaps
}

func scaleTicks(v int64, from, to media.Timescale) int64 {
	if from <= 0 {
		return 0
	}
	n := v * int64(to)
	q := n / int64(from)
	if r := n % int64(from); r*2 >= int64(from) {
		q++
	}
	return q
}

// sliceReadSeeker adapts a byte slice to io.ReadSeeker for go-mp4.
type sliceReadSeeker struct {
	data []byte
	off  int64
}

func (s *sliceReadSeeker) Read(p []byte) (int, error) {
	if s.off >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.off:])
	s.off += int64(n)
	return n, nil
}

func (s *sliceReadSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.off = offset
	case io.SeekCurrent:
		s.off += offset
	case io.SeekEnd:
		s.off = int64(len(s.data)) + offset
	}
	if s.off < 0 {
		return 0, fmt.Errorf("negative seek")
	}
	return s.off, nil
}

// trackIDFor fills in a track id for samples that were parsed before the
// enclosing tkhd was seen, using the single known track when unambiguous.
func trackIDFor(tracks []TrackInfo, id uint32) uint32 {
	if id != 0 || len(tracks) != 1 {
		return id
	}
	return tracks[0].ID
}
