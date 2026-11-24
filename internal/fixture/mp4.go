package fixture

// Sample describes one synthetic media sample. Keyframe controls the
// trun sample_flags sync bit; Duration is in track timescale units.
type Sample struct {
	Duration uint32
	Size     uint32
	Keyframe bool
	// Payload seeds the deterministic sample body; adjacent samples get
	// distinct, reproducible content.
	Payload byte
}

// TrackConfig parameterizes an initialization segment.
type TrackConfig struct {
	TrackID   uint32
	Timescale uint32
	Width     uint16
	Height    uint16
}

// InitSegment builds a minimal fMP4 initialization segment containing
// ftyp, moov(mvhd, trak(tkhd, mdia(mdhd, hdlr, minf(stbl(...))))).
func InitSegment(cfg TrackConfig) []byte {
	if cfg.Timescale == 0 {
		cfg.Timescale = 1000
	}
	ftyp := Box{Type: "ftyp", Data: append([]byte("iso5"),
		[]byte("\x00\x00\x00\x02isomiso5dash")...)}
	mvhd := Box{Type: "mvhd", Data: buildMvhd(cfg)}
	tkhd := Box{Type: "tkhd", Data: buildTkhd(cfg)}
	mdhd := Box{Type: "mdhd", Data: buildMdhd(cfg)}
	hdlr := Box{Type: "hdlr",
		Data: append(append(fullBox(0, 0), 0, 0, 0, 0), []byte("vide\x00\x00\x00\x00\x00\x00\x00\x00\x00VideoHandler\x00")...)}
	stsd := Box{Type: "stsd", Data: buildStsd(cfg)}
	stbl := Box{Type: "stbl", Children: []Box{
		stsd,
		{Type: "stts", Data: append(fullBox(0, 0), 0, 0, 0, 0)},
		{Type: "stsc", Data: append(fullBox(0, 0), 0, 0, 0, 0)},
		{Type: "stsz", Data: append(fullBox(0, 0), 0, 0, 0, 0, 0, 0, 0, 0)},
		{Type: "stco", Data: append(fullBox(0, 0), 0, 0, 0, 0)},
	}}
	minf := Box{Type: "minf", Children: []Box{
		{Type: "vmhd", Data: fullBox(0, 1)},
		{Type: "dinf", Children: []Box{{Type: "dref", Data: buildDref()}}},
		stbl,
	}}
	mdia := Box{Type: "mdia", Children: []Box{mdhd, hdlr, minf}}
	trak := Box{Type: "trak", Children: []Box{tkhd, mdia}}
	moov := Box{Type: "moov", Children: []Box{mvhd, trak}}
	return append(ftyp.Encode(), moov.Encode()...)
}

// MediaSegment builds one fMP4 media segment: styp + moof(tfhd, tfdt,
// trun) + mdat(sample bodies). baseDecodeTime uses 64-bit tfdt (v1) so
// wrap-expanded or very large timestamps survive a round trip.
func MediaSegment(trackID, seq uint32, baseDecodeTime uint64, samples []Sample) []byte {
	tfhd := Box{Type: "tfhd", Data: buildTfhd(trackID, 0x020000 /*default-base-is-moof*/)}
	tfdt := Box{Type: "tfdt", Data: buildTfdt(baseDecodeTime)}
	trun := Box{Type: "trun", Data: buildTrun(samples, true)}
	traf := Box{Type: "traf", Children: []Box{tfhd, tfdt, trun}}
	moof := Box{Type: "moof", Children: []Box{
		{Type: "mfhd", Data: buildMfhd(seq)}, traf,
	}}
	mdatBody := make([]byte, 0)
	for _, s := range samples {
		body := make([]byte, s.Size)
		for i := range body {
			body[i] = s.Payload + byte(i%251)
		}
		mdatBody = append(mdatBody, body...)
	}
	mdat := Box{Type: "mdat", Data: mdatBody}

	head := append(Box{Type: "styp", Data: []byte("msdh\x00\x00\x00\x00msdh")}.Encode(),
		moof.Encode()...)
	// Data offset counts from the moof start and must point at the first
	// sample inside mdat (i.e. past the mdat 8-byte header).
	moofLen := uint32(len(moof.Encode()))
	dataOffset := int32(moofLen) + 8
	patchTrunDataOffset(head, dataOffset)
	return append(head, mdat.Encode()...)
}
