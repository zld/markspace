package mp4

import (
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediascan/internal/model"
)

// buildFragments decodes every moof/traf group in file order.
func buildFragments(f io.ReaderAt, hs []BoxHeader, ts uint32, segment string) ([]FragmentInfo, []model.Fault) {
	var faults []model.Fault
	var frags []FragmentInfo
	var cur *FragmentInfo
	var curMoofOffset int64

	for i := range hs {
		h := &hs[i]
		switch h.Type {
		case "moof":
			frags = append(frags, FragmentInfo{BoxOffset: h.Offset})
			cur = &frags[len(frags)-1]
			curMoofOffset = h.Offset
		case "mfhd":
			if cur == nil {
				continue
			}
			var mfhd gomp4.Mfhd
			if err := unmarshalHeader(f, h, &mfhd); err == nil {
				cur.Sequence = mfhd.SequenceNumber
			}
		case "tfdt":
			if cur == nil {
				continue
			}
			var tfdt gomp4.Tfdt
			if err := unmarshalHeader(f, h, &tfdt); err != nil {
				faults = append(faults, badTableFault(segment, h, fmt.Sprintf("tfdt: %v", err)))
				continue
			}
			cur.BaseDecodeRaw = tfdt.GetBaseMediaDecodeTime()
		case "trun":
			if cur == nil {
				continue
			}
			var trun gomp4.Trun
			if err := unmarshalHeader(f, h, &trun); err != nil {
				faults = append(faults, badTableFault(segment, h, fmt.Sprintf("trun: %v", err)))
				continue
			}
			baseOff := int64(model.Unknown)
			if trun.GetFlags()&0x000001 != 0 {
				baseOff = curMoofOffset + int64(trun.DataOffset)
			}
			var cum int64
			for j, e := range trun.Entries {
				off := int64(model.Unknown)
				if baseOff >= 0 {
					off = baseOff + cum
				}
				d := e.SampleDuration
				cur.Samples = append(cur.Samples, Sample{
					Index:         int64(j),
					Offset:        off,
					Size:          e.SampleSize,
					Delta:         d,
					TimeSource:    int64(cur.BaseDecodeRaw) + cum,
					TimeUnified:   toUnified(int64(cur.BaseDecodeRaw)+cum, ts, segment),
					DurationTicks: toUnified(int64(d), ts, segment),
					Sync:          j == 0, // first sample of a fragment is a SAP
				})
				cum += int64(d)
			}
		}
	}
	return frags, faults
}
