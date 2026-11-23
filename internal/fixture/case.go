package fixture

// Delivery is one runnable demo case: inputs plus the canonical expected
// shape used by assertions.
type Delivery struct {
	Name         string
	MPD          []byte
	Playlist     []byte
	Media        map[string][]byte
	Reps         int
	Segments     int
	BoundariesMS []int
}

// repsFor is the standard three-rendition ladder at three time bases.
func repsFor() []Representation {
	return []Representation{
		{ID: "v1080", Bandwidth: 5000000, MimeType: "video/mp4", Codecs: "avc1.640028",
			Timescale: 90000, SegTicks: 180000, BoundariesMS: HLSBoundaries},
		{ID: "v720", Bandwidth: 2500000, MimeType: "video/mp4", Codecs: "avc1.4d401f",
			Timescale: 1000, SegTicks: 2000, BoundariesMS: HLSBoundaries},
		{ID: "a48", Bandwidth: 128000, MimeType: "audio/mp4", Codecs: "mp4a.40.2",
			Timescale: 48000, SegTicks: 96000, BoundariesMS: HLSBoundaries},
	}
}

// Normal builds the clean three-rendition, twelve-segment case: one
// fragment per HLS interval, so every interval boundary is also a fragment
// boundary and drift is exactly zero.
func Normal() Delivery {
	reps := repsFor()
	media := map[string][]byte{}
	total := 0
	track := uint32(1)
	for _, r := range reps {
		specs := SegmentsFor(HLSBoundaries, r.Timescale, track, sampleCountFor(r.Timescale))
		media[r.ID] = SegmentFile(specs)
		total += len(specs)
		track++
	}
	return Delivery{
		Name:     "normal",
		MPD:      MPDEscaped("PT8S", reps),
		Playlist: PlaylistFor(HLSBoundaries, 2, false),
		Media:    media,
		Reps:     3, Segments: total, BoundariesMS: HLSBoundaries,
	}
}

// Damaged builds a case whose inputs carry all five injectable faults, one
// per class, across four representations:
//
//	truncated_box       v1080's final fragment is cut mid-mdat
//	invalid_box_size    v720's fourth fragment declares an impossible size
//	zero_timescale      the v360 representation index declares timescale 0
//	duplicate_sequence  the playlist restarts its media sequence after the
//	                    discontinuity
//	missing_index       the a48 representation has no segment template
func Damaged() Delivery {
	normal := Normal()
	reps := repsFor()

	// Media for the two extra representations, so the MPD faults are not
	// the only evidence for them.
	media := map[string][]byte{}
	for id, data := range normal.Media {
		media[id] = data
	}
	media["v360"] = SegmentFile(SegmentsFor(HLSBoundaries, 90000, 3, 3))

	// v1080: cut inside the final mdat, leaving earlier fragments intact.
	v1080 := normal.Media["v1080"]
	media["v1080"] = TruncateBox(v1080, int64(len(v1080))-60)

	// v720: overwrite the fourth fragment's moof size.
	v720 := normal.Media["v720"]
	offsets := TopLevelOffsets(v720)
	badOffset := offsets[6] // fourth moof (styp, moof, mdat, ...)
	media["v720"] = CorruptBoxSize(v720, badOffset, 0xFFFFFF00)
	_ = reps

	return Delivery{
		Name:         "damaged",
		MPD:          FaultyMPD(true, true),
		Playlist:     PlaylistFor(HLSBoundaries, 2, true),
		Media:        media,
		Reps:         4,
		Segments:     normal.Segments,
		BoundariesMS: HLSBoundaries,
	}
}
