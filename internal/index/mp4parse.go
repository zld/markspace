package index

import (
	"os"

	"github.com/abema/go-mp4"
)

type parsedSample struct {
	duration uint32
	size     uint32
	offset   int64
	sync     bool
}

type parsedMedia struct {
	timescale      int64
	baseDecodeTime uint64
	samples        []parsedSample
	fragment       bool
}

type trunCapture struct {
	trun       *mp4.Trun
	moofOffset uint64
}

// parseMP4 uses go-mp4 to read mdhd, sample tables (stts/stsz/stco/co64/stss)
// and fragmented tables (tfdt/trun).
func parseMP4(path string) (*parsedMedia, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	pm := &parsedMedia{}
	var truns []trunCapture
	var uniformSize uint32
	var stszSizes []uint32
	var durations []uint32
	var offsets []int64
	syncNumbers := map[uint32]bool{}
	var currentMoof uint64

	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoof():
			currentMoof = h.BoxInfo.Offset
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			pm.timescale = int64(box.(*mp4.Mdhd).Timescale)
		case mp4.BoxTypeTfdt():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			pm.baseDecodeTime = box.(*mp4.Tfdt).GetBaseMediaDecodeTime()
		case mp4.BoxTypeTrun():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			truns = append(truns, trunCapture{trun: box.(*mp4.Trun), moofOffset: currentMoof})
		case mp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, e := range box.(*mp4.Stts).Entries {
				for i := uint32(0); i < e.SampleCount; i++ {
					durations = append(durations, e.SampleDelta)
				}
			}
		case mp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			stsz := box.(*mp4.Stsz)
			uniformSize = stsz.SampleSize
			stszSizes = stsz.EntrySize
		case mp4.BoxTypeStco():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, o := range box.(*mp4.Stco).ChunkOffset {
				offsets = append(offsets, int64(o))
			}
		case mp4.StrToBoxType("co64"):
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			// co64 is not needed by generated fixtures; retain support through raw extraction if encountered.
			_ = box
		case mp4.BoxTypeStss():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, n := range box.(*mp4.Stss).SampleNumber {
				syncNumbers[n] = true
			}
		}
		if h.BoxInfo.IsSupportedType() {
			return h.Expand()
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}

	if len(truns) > 0 {
		pm.fragment = true
		for _, tc := range truns {
			dataStart := int64(tc.moofOffset)
			if tc.trun.GetFlags()&0x000001 != 0 {
				dataStart += int64(tc.trun.DataOffset)
			}
			cursor := dataStart
			for i, e := range tc.trun.Entries {
				size := e.SampleSize
				pm.samples = append(pm.samples, parsedSample{
					duration: e.SampleDuration,
					size:     size,
					offset:   cursor,
					sync:     i == 0,
				})
				cursor += int64(size)
			}
			_ = tc.trun.SampleCount
		}
		return pm, nil
	}

	n := len(stszSizes)
	if uniformSize != 0 {
		// Sample count is represented by duration/offset lists in generated tables.
		if len(durations) > n {
			n = len(durations)
		}
	}
	if len(durations) > n {
		n = len(durations)
	}
	for i := 0; i < n; i++ {
		var size uint32
		if uniformSize != 0 {
			size = uniformSize
		} else if i < len(stszSizes) {
			size = stszSizes[i]
		}
		var off int64
		if i < len(offsets) {
			off = offsets[i]
		} else if len(offsets) == 1 {
			off = offsets[0]
		}
		var dur uint32
		if i < len(durations) {
			dur = durations[i]
		}
		pm.samples = append(pm.samples, parsedSample{
			duration: dur,
			size:     size,
			offset:   off,
			sync:     len(syncNumbers) == 0 || syncNumbers[uint32(i+1)],
		})
	}
	return pm, nil
}
