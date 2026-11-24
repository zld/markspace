package fixgen

// SampleSpec is one media sample inside a fragment.
type SampleSpec struct {
	Data     []byte
	Duration uint32 // in track timescale ticks
}

// FragSpec is one moof+mdat pair for a single track.
type FragSpec struct {
	Seq     uint32 // mfhd sequence number
	TrackID uint32
	TFDT    uint64 // base media decode time, track timescale ticks
	Samples []SampleSpec
}

// FileSpec describes a complete fragmented MP4 file.
type FileSpec struct {
	Tracks []TrackSpec
	Frags  []FragSpec
	// TruncateAt, when > 0, cuts the file to exactly this many bytes,
	// producing a truncated trailing box.
	TruncateAt int
}

// mfhd serializes the movie fragment header.
func mfhd(seq uint32) []byte {
	return fullBox("mfhd", 0, 0, u32(seq))
}

// tfhd uses default-base-is-moof so no absolute offsets are needed.
func tfhd(trackID uint32) []byte {
	return fullBox("tfhd", 0, 0x020000, u32(trackID))
}

func tfdt(v uint64) []byte {
	return fullBox("tfdt", 1, 0, u64(v))
}

// trun carries data-offset + per-sample duration and size.
func trun(dataOffset int32, samples []SampleSpec) []byte {
	const flags = 0x000001 | 0x000100 | 0x000200 // data-offset, duration, size
	payload := concat(u32(uint32(len(samples))), u32(uint32(dataOffset)))
	for _, s := range samples {
		payload = append(payload, concat(u32(s.Duration), u32(uint32(len(s.Data))))...)
	}
	return fullBox("trun", 0, flags, payload)
}

// moofMdat builds one fragment; the trun data-offset points from the moof
// start to the mdat payload.
func moofMdat(f FragSpec) []byte {
	traf := box("traf", concat(tfhd(f.TrackID), tfdt(f.TFDT), trun(0, f.Samples)))
	moofLen := 8 + len(mfhd(f.Seq)) + len(traf)
	dataOffset := int32(moofLen + 8) // moof + mdat header
	traf = box("traf", concat(tfhd(f.TrackID), tfdt(f.TFDT), trun(dataOffset, f.Samples)))
	moof := box("moof", concat(mfhd(f.Seq), traf))
	var mdatPayload []byte
	for _, s := range f.Samples {
		mdatPayload = append(mdatPayload, s.Data...)
	}
	return concat(moof, box("mdat", mdatPayload))
}

// Build renders the complete file bytes.
func Build(spec FileSpec) []byte {
	out := concat(ftyp(), moov(spec.Tracks))
	for _, f := range spec.Frags {
		out = append(out, moofMdat(f)...)
	}
	if spec.TruncateAt > 0 && spec.TruncateAt < len(out) {
		out = out[:spec.TruncateAt]
	}
	return out
}
