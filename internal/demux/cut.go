package demux

// addCut records an incomplete input range once.  Duplicate and empty ranges
// are dropped so downstream output is stable across tolerant reparses.
func addCut(media *Media, r Range) {
	if r.End <= r.Start {
		return
	}
	for _, existing := range media.Cut {
		if existing == r {
			return
		}
	}
	media.Cut = append(media.Cut, r)
}
