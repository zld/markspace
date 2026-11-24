package fixture

// BuildValid returns the normal multi-track fixture.
func BuildValid(opts Options) Payload {
	tracks := []trackSpec{
		{
			id: 1, kind: "video", handler: "vide", timescale: 90000,
			stsd: stsd(visualSampleEntry("avc1", avcC())),
			samples: []sampleSpec{
				{data: seiPayload("mp-sei-1"), delta: 3000, sync: true},
				{data: seiPayload("mp-sei-2"), delta: 3000, sync: false},
				{data: seiPayload("mp-sei-3"), delta: 3000, sync: true},
			},
		},
		{
			id: 2, kind: "audio", handler: "soun", timescale: 48000,
			stsd:    stsd(audioSampleEntry()),
			samples: []sampleSpec{{data: audioPayload, delta: 1024, sync: true}, {data: audioPayload, delta: 1024, sync: true}},
		},
		{
			id: 3, kind: "subtitle", handler: "subt", timescale: 1000,
			stsd:    stsd(subtitleSampleEntry()),
			samples: []sampleSpec{{data: subPayload, delta: 500, sync: true}, {data: subPayload, delta: 500, sync: true}},
		},
		{
			id: 4, kind: "chapter", handler: "text", timescale: 1000,
			stsd:    stsd(metadataSampleEntry("tx3g")),
			samples: []sampleSpec{{data: chapterPayload, delta: 1000, sync: true}},
		},
		{
			id: 5, kind: "metadata", handler: "meta", timescale: 1000,
			stsd:    stsd(metadataSampleEntry("ID3 ")),
			samples: []sampleSpec{{data: id3Payload, delta: 1000, sync: true}},
		},
	}

	if opts.Fragmented {
		return Payload{Name: "valid-fragmented.mp4", Data: buildFragmented(tracks, opts)}
	}
	return Payload{Name: "valid.mp4", Data: buildClassic(tracks, opts)}
}

func emsgFixtureBoxes(opts Options) [][]byte {
	events := []struct {
		scheme, value string
		time          uint64
		data          string
	}{
		{"urn:mp:cue", "1", 2000, "cue-a"},
		{"urn:mp:cue", "2", 1000, "cue-b"},
	}
	if opts.EventDuplicates {
		events = append(events, events[0])
	}
	var out [][]byte
	for _, e := range events {
		var b wbuf
		b.full(1, 0)
		b.u32(1000)
		b.u64(e.time)
		b.u32(0)
		b.u32(0)
		b.str(e.scheme)
		b.u8(0)
		b.str(e.value)
		b.u8(0)
		b.str(e.data)
		out = append(out, box("emsg", b.bytes()))
	}
	return out
}

func udtaChapterBox() []byte {
	var b wbuf
	b.full(0, 0)
	b.u8(0)
	b.u32(1)
	b.u64(0)
	b.u8(1)
	b.str("chapter")
	b.u8(0)
	chpl := box("chpl", b.bytes())
	return container("udta", chpl)
}

// Options controls the generated valid fixture.
type Options struct {
	// EventDuplicates adds a duplicate emsg with identical content and time so
	// the dedup result can be asserted as exactly one kept copy.
	EventDuplicates bool
	// Fragmented produces a moof/mdat file with a mfra index instead of a
	// classic sample table.
	Fragmented bool
	// DropIndex removes the index from a fragmented file.
	DropIndex bool
}
