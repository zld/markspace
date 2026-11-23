package testdata

// sttsBox merges consecutive equal deltas into stts run entries.
func sttsBox(deltas []uint32) []byte {
	type run struct{ count, delta uint32 }
	var runs []run
	for _, d := range deltas {
		if len(runs) > 0 && runs[len(runs)-1].delta == d {
			runs[len(runs)-1].count++
		} else {
			runs = append(runs, run{1, d})
		}
	}
	p := fullbox(0, 0, nil)
	tmp := make([]byte, 4)
	putU32(tmp, uint32(len(runs)))
	p = append(p, tmp...)
	for _, r := range runs {
		putU32(tmp, r.count)
		p = append(p, tmp...)
		putU32(tmp, r.delta)
		p = append(p, tmp...)
	}
	return box("stts", p)
}

// cttsBox emits a signed version-1 composition offset box, or nil when zero.
func cttsBox(offsets []int64) []byte {
	any := false
	for _, o := range offsets {
		if o != 0 {
			any = true
		}
	}
	if !any {
		return nil
	}
	p := fullbox(1, 0, nil)
	tmp := make([]byte, 4)
	putU32(tmp, uint32(len(offsets)))
	p = append(p, tmp...)
	for _, o := range offsets {
		putU32(tmp, 1) // sample_count: one sample per entry
		p = append(p, tmp...)
		putI32(tmp, int32(o)) // sample_offset (signed, version 1)
		p = append(p, tmp...)
	}
	return box("ctts", p)
}

func stscBox(samples int) []byte {
	p := fullbox(0, 0, nil)
	tmp := make([]byte, 4)
	putU32(tmp, 1)
	p = append(p, tmp...) // one entry
	putU32(tmp, 1)
	p = append(p, tmp...) // first chunk
	putU32(tmp, 1)
	p = append(p, tmp...) // one sample per chunk
	putU32(tmp, 1)
	p = append(p, tmp...) // description index
	return box("stsc", p)
}

func stszBox(sizes []uint32) []byte {
	p := fullbox(0, 0, nil)
	tmp := make([]byte, 4)
	putU32(tmp, 0)
	p = append(p, tmp...) // sample_size=0
	putU32(tmp, uint32(len(sizes)))
	p = append(p, tmp...)
	for _, s := range sizes {
		putU32(tmp, s)
		p = append(p, tmp...)
	}
	return box("stsz", p)
}

func stcoBox(offsets []uint32) []byte {
	p := fullbox(0, 0, nil)
	tmp := make([]byte, 4)
	putU32(tmp, uint32(len(offsets)))
	p = append(p, tmp...)
	for _, o := range offsets {
		putU32(tmp, o)
		p = append(p, tmp...)
	}
	return box("stco", p)
}

func stssBox(keySampleNumbers []uint32) []byte {
	p := fullbox(0, 0, nil)
	tmp := make([]byte, 4)
	putU32(tmp, uint32(len(keySampleNumbers)))
	p = append(p, tmp...)
	for _, n := range keySampleNumbers {
		putU32(tmp, n)
		p = append(p, tmp...)
	}
	return box("stss", p)
}

// elstBox emits a single-entry version-0 edit list.
func elstBox(startUnits, durationUnits uint64) []byte {
	p := fullbox(0, 0, nil)
	tmp := make([]byte, 4)
	putU32(tmp, 1)
	p = append(p, tmp...) // one entry
	putU32(tmp, uint32(durationUnits))
	p = append(p, tmp...)
	putI32(tmp, int32(startUnits))
	p = append(p, tmp...)
	putI32(tmp, 0x00010000) // rate 1.0
	p = append(p, tmp...)
	return box("elst", p)
}

func stsd(entry []byte) []byte {
	p := fullbox(0, 0, nil)
	p = append(p, 0, 0, 0, 1)
	p = append(p, entry...)
	return box("stsd", p)
}

func dref() []byte {
	entry := box("url ", fullbox(0, 1, nil)) // self-contained flag
	p := fullbox(0, 0, nil)
	p = append(p, 0, 0, 0, 1)
	p = append(p, entry...)
	return box("dref", p)
}

func dinf() []byte { return box("dinf", dref()) }
