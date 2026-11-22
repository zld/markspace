package bitstream

// Index is a keyframe index: absolute byte offsets of IDR NAL units.
type Index struct {
	Offsets []int64
}

// Build derives the ground-truth keyframe index from a scan.
func Build(res *ScanResult) Index {
	idx := Index{Offsets: make([]int64, 0, len(res.Keyframes))}
	for _, k := range res.Keyframes {
		idx.Offsets = append(idx.Offsets, res.NALs[k].Offset)
	}
	return idx
}

// Verify checks the index against a (re)scan. It returns the number of
// entries that match a scanned IDR and the offsets that did not.
func (idx Index) Verify(res *ScanResult) (hits int, missing []int64) {
	truth := make(map[int64]bool, len(res.Keyframes))
	for _, k := range res.Keyframes {
		truth[res.NALs[k].Offset] = true
	}
	for _, off := range idx.Offsets {
		if truth[off] {
			hits++
		} else {
			missing = append(missing, off)
		}
	}
	return hits, missing
}

// HitRate returns the fraction of index entries confirmed by the scan,
// or 1 when the index is empty.
func (idx Index) HitRate(res *ScanResult) float64 {
	if len(idx.Offsets) == 0 {
		return 1
	}
	hits, _ := idx.Verify(res)
	return float64(hits) / float64(len(idx.Offsets))
}

// Intervals returns the byte distances between adjacent indexed
// keyframes, in index order.
func (idx Index) Intervals() []int64 {
	if len(idx.Offsets) < 2 {
		return nil
	}
	out := make([]int64, 0, len(idx.Offsets)-1)
	for i := 1; i < len(idx.Offsets); i++ {
		out = append(out, idx.Offsets[i]-idx.Offsets[i-1])
	}
	return out
}
