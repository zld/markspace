package demux

import "mediapipeline/internal/fault"

func findTop(boxes []*rawBox, typ string) *rawBox {
	for _, b := range boxes {
		if b.typ == typ {
			return b
		}
	}
	return nil
}

func findTops(boxes []*rawBox, typ string) []*rawBox {
	var out []*rawBox
	for _, b := range boxes {
		if b.typ == typ {
			out = append(out, b)
		}
	}
	return out
}

func hasIndex(top []*rawBox) bool {
	if findTop(top, "sidx") != nil {
		return true
	}
	mfra := findTop(top, "mfra")
	if mfra == nil {
		return false
	}
	return mfra.find("tfra") != nil
}

// trimSamplesBefore drops the samples that lie beyond the first cut byte.  A
// truncated input keeps its verified prefix; the incomplete range is reported
// rather than silently dropped.
func trimSamplesBefore(tracks []Track, cutStart int64) error {
	for ti := range tracks {
		tr := &tracks[ti]
		keep := 0
		for _, s := range tr.Samples {
			if s.Offset+int64(s.Size) > cutStart {
				break
			}
			keep++
		}
		tr.Samples = tr.Samples[:keep]
	}
	return nil
}

// movieTimescaleOf returns the mvhd timescale.
func movieTimescaleOf(moov *rawBox) uint32 {
	mvhd := moov.find("mvhd")
	if mvhd == nil {
		return 0
	}
	r := newBinReader(mvhd, fault.OpBoxHeader)
	version, _, err := r.fullBox()
	if err != nil {
		return 0
	}
	skip := 8
	if version == 1 {
		skip = 16
	}
	if err := r.skip(skip); err != nil {
		return 0
	}
	ts, err := r.u32()
	if err != nil {
		return 0
	}
	return ts
}
