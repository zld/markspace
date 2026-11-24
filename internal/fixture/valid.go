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
