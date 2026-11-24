package fixture

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
