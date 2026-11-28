package container

// extent is a half-open byte range.
type extent struct {
	start, end int64
}

// indexedExtents collects the byte ranges the sample tables already account for,
// so boundary recovery does not double-count the same bytes.
func indexedExtents(tracks []Track) []extent {
	var out []extent
	for _, track := range tracks {
		for _, sample := range track.Samples {
			if !sample.Recovered {
				out = append(out, extent{start: sample.Offset, end: sample.Offset + int64(sample.Size)})
			}
		}
	}
	return out
}

// overlapsIndexed reports whether [start, end) intersects any indexed extent.
func overlapsIndexed(covered []extent, start, end int64) bool {
	for _, e := range covered {
		if start < e.end && e.start < end {
			return true
		}
	}
	return false
}

// recoverFromMdats turns mdat bodies into fixed-size recovered samples, skipping
// bytes the index already describes and continuing the recovered timeline across
// regions.
func recoverFromMdats(regions []mdatRegion, covered []extent, options ScanOptions) ([]Sample, int64) {
	var samples []Sample
	var bytes int64
	for _, region := range regions {
		for offset := region.Start; offset < region.End; offset += options.BoundarySampleSize {
			if len(samples) >= options.MaxSamplesPerTrack {
				return samples, bytes
			}
			end := offset + options.BoundarySampleSize
			if end > region.End {
				end = region.End
			}
			size := end - offset
			if size <= 0 {
				break
			}
			if overlapsIndexed(covered, offset, end) {
				continue
			}
			samples = append(samples, Sample{
				TrackID:    recoveredTrackID,
				Number:     uint32(len(samples) + 1),
				Offset:     offset,
				Size:       uint32(size),
				StartNS:    int64(len(samples)) * options.BoundarySampleDurationNS,
				DurationNS: options.BoundarySampleDurationNS,
				Sync:       true,
				Recovered:  true,
			})
			bytes += size
		}
	}
	return samples, bytes
}
