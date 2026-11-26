// Package output verifies and publishes delivery artifacts. MP4 output files
// are independently re-parsed with go-mp4 (not trusted from FFmpeg's report)
// so the manifest can cite concrete sample counts, sizes and offsets, and a
// later `verify` run can detect any on-disk drift.
package output

import (
	"fmt"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// MP4Info is the independently verified sample-table summary of one M4A.
type MP4Info struct {
	Timescale      uint32
	Duration       uint64
	SampleCount    uint32
	SumSampleSizes uint64
	FirstOffset    uint64
	Brands         []string
}

// InspectMP4 opens path and walks its box structure to recover the audio
// sample table. Any structural problem is returned as an error rather than
// partially populated info.
func InspectMP4(path string) (*MP4Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open mp4: %w", err)
	}
	defer f.Close()

	info := &MP4Info{}
	handled := map[string]bool{}
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		boxType := h.BoxInfo.Type.String()
		switch boxType {
		case "ftyp", "mdhd", "stsz", "stco", "co64":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			switch b := box.(type) {
			case *mp4.Ftyp:
				info.Brands = append(info.Brands, string(b.MajorBrand[:]))
			case *mp4.Mdhd:
				info.Timescale = b.Timescale
				info.Duration = b.GetDuration()
				handled["mdhd"] = true
			case *mp4.Stsz:
				info.SampleCount = b.SampleCount
				if b.SampleSize != 0 {
					info.SumSampleSizes = uint64(b.SampleSize) * uint64(b.SampleCount)
				} else {
					for _, sz := range b.EntrySize {
						info.SumSampleSizes += uint64(sz)
					}
				}
				handled["stsz"] = true
			case *mp4.Stco:
				if b.EntryCount > 0 {
					info.FirstOffset = uint64(b.ChunkOffset[0])
					handled["stco"] = true
				}
			case *mp4.Co64:
				if b.EntryCount > 0 {
					info.FirstOffset = b.ChunkOffset[0]
					handled["stco"] = true
				}
			}
			return nil, nil
		default:
			// Descend into container boxes; leaf boxes need no expansion.
			if h.BoxInfo.IsSupportedType() {
				return h.Expand()
			}
			return nil, nil
		}
	})
	if err != nil {
		return nil, fmt.Errorf("parse mp4 %s: %w", path, err)
	}
	for _, want := range []string{"mdhd", "stsz", "stco"} {
		if !handled[want] {
			return nil, fmt.Errorf("mp4 %s missing required box: %s", path, want)
		}
	}
	return info, nil
}

// ByteDrift returns the number of bytes after the end of the mdat box. A
// non-zero drift means the output carries unexpected trailing data and must
// not be published. Faststart files place mdat before moov, so the check
// works regardless of box order.
func ByteDrift(path string, info *MP4Info) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	end, err := mdatEnd(path)
	if err != nil {
		return 0, err
	}
	return fi.Size() - end, nil
}

func mdatEnd(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var end int64
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type.String() == "mdat" {
			end = int64(h.BoxInfo.Offset + h.BoxInfo.Size)
			return nil, nil
		}
		if h.BoxInfo.IsSupportedType() {
			return h.Expand()
		}
		return nil, nil
	})
	if err != nil {
		return 0, err
	}
	if end == 0 {
		return 0, fmt.Errorf("mdat box not found in %s", path)
	}
	return end, nil
}
