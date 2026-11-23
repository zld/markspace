package fixture

import "errors"

// GenerateMP4 serializes the documented MP4 subset. The returned bytes are
// deterministic for a given spec; Layout records every sample offset so
// damaged variants can corrupt precise byte ranges.
func GenerateMP4(spec MediaSpec) ([]byte, *Layout, error) {
	if len(spec.Tracks) == 0 {
		return nil, nil, errors.New("fixture: media spec has no tracks")
	}
	plans := make([]trackPlan, len(spec.Tracks))
	for i, ts := range spec.Tracks {
		if ts.FrameSamples == 0 {
			ts.FrameSamples = spec.FrameSamples
		}
		plans[i] = trackPlan{spec: ts}
	}
	// First pass builds moov to determine the mdat payload start offset.
	moov, err := buildMoov(spec, plans, nil)
	if err != nil {
		return nil, nil, err
	}
	ftyp := buildFtyp()
	const mdatHeaderLen = 8
	mdatPayloadStart := int64(len(ftyp) + len(moov) + mdatHeaderLen)

	// Second pass: interleave chunks and fill offsets.
	var mdat []byte
	layout := &Layout{Tracks: map[int]*TrackLayout{}}
	maxChunks := 0
	for _, p := range plans {
		if n := chunksFor(p.spec, p.spec.FrameSamples); n > maxChunks {
			maxChunks = n
		}
	}
	for chunk := 0; chunk < maxChunks; chunk++ {
		for ti := range plans {
			p := &plans[ti]
			from, to := chunkRange(p.spec, chunk, p.spec.FrameSamples)
			if from >= to {
				continue
			}
			chunkOff := mdatPayloadStart + int64(len(mdat))
			tl, seen := layout.Tracks[p.spec.ID]
			if !seen {
				tl = &TrackLayout{TrackID: p.spec.ID}
				layout.Tracks[p.spec.ID] = tl
			}
			tl.ChunkOffset = append(tl.ChunkOffset, chunkOff)
			for si := from; si < to; si++ {
				seed := uint32(spec.Seed)*1000003 + uint32(p.spec.ID)*97 + uint32(si)
				payload := SamplePayload(p.spec.ID, si, int(seed), p.spec.SampleSize)
				abs := mdatPayloadStart + int64(len(mdat))
				p.offsets = append(p.offsets, abs)
				p.sizes = append(p.sizes, int64(len(payload)))
				tl.SampleOffset = append(tl.SampleOffset, abs)
				tl.SampleSize = append(tl.SampleSize, int64(len(payload)))
				mdat = append(mdat, payload...)
			}
		}
	}
	layout.MdatOffset = mdatPayloadStart - mdatHeaderLen
	layout.MdatPayload = int64(len(mdat))

	// Third pass: rebuild moov now that chunk offsets are known.
	moov, err = buildMoov(spec, plans, layout)
	if err != nil {
		return nil, nil, err
	}
	out := make([]byte, 0, len(ftyp)+len(moov)+8+len(mdat))
	out = append(out, ftyp...)
	out = append(out, moov...)
	out = appendBox(out, "mdat", mdat)
	return out, layout, nil
}

func buildFtyp() []byte {
	payload := make([]byte, 0, 16)
	payload = append(payload, []byte("isom")...)
	payload = append(payload, u32(0x200)...)
	payload = append(payload, []byte("isom")...)
	payload = append(payload, []byte("iso2")...)
	return appendBox(nil, "ftyp", payload)
}
