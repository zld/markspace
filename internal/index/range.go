package index

// RangeStatus classifies a range read result.
type RangeStatus string

const (
	StatusOK         RangeStatus = "ok"
	StatusEmptyRange RangeStatus = "empty_range"
	StatusOutOfMedia RangeStatus = "out_of_media"
)

// RangeResult is the outcome of a [fromNs,toNs) read: the hit events plus
// the first and last boundary of the hit span.
type RangeResult struct {
	Status          RangeStatus `json:"status"`
	Hits            []Entry     `json:"hits"`
	FirstBoundaryNs int64       `json:"first_boundary_ns"`
	LastBoundaryNs  int64       `json:"last_boundary_ns"`
}

// Read returns the entries intersecting [fromNs,toNs). Empty ranges and
// ranges outside the media span are reported with distinct statuses.
func (ix *Index) Read(fromNs, toNs int64) RangeResult {
	if fromNs >= toNs {
		return RangeResult{Status: StatusEmptyRange}
	}
	if len(ix.Entries) == 0 || toNs <= ix.MediaStartNs || fromNs >= ix.MediaEndNs {
		return RangeResult{Status: StatusOutOfMedia}
	}
	res := RangeResult{Status: StatusOK}
	for _, e := range ix.Entries {
		if e.StartNs < toNs && e.EndNs > fromNs {
			res.Hits = append(res.Hits, e)
		}
	}
	if len(res.Hits) > 0 {
		res.FirstBoundaryNs = res.Hits[0].StartNs
		res.LastBoundaryNs = res.Hits[len(res.Hits)-1].EndNs
	}
	return res
}
