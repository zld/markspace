package mp4

// FragmentSpec describes one deterministic output fragment.
type FragmentSpec struct {
	Seq             int
	TrackID         uint32
	BaseMediaDecode uint64 // tfdt, track timescale units
	// TfdtV0 selects the 32-bit tfdt version; false emits version 1.
	TfdtV0          bool
	SampleDurations []uint32
	SamplePayload   []byte // concatenated compressed sample bytes
	SampleSizes     []uint32
}

// BuildTfdt writes version-1 tfdt for full uint64 base media decode time.
func BuildTfdt(base uint64) []byte {
	return BuildFullBox("tfdt", 1, 0, u64be(base))
}

// BuildTfdtV0 writes a 32-bit version-0 tfdt, matching devices whose
// timestamps can legally wrap at the 2^32 boundary.
func BuildTfdtV0(base uint32) []byte {
	return BuildFullBox("tfdt", 0, 0, u32be(base))
}

// BuildMfhd writes the fragment sequence number.
func BuildMfhd(seq int) []byte {
	return BuildFullBox("mfhd", 0, 0, u32be(uint32(seq)))
}

// BuildTraf writes one traf containing tfhd/tfdt/trun. The trun
// data-offset is computed against the eventual mdat layout: it points at
// the first payload byte relative to the start of the mdat box.
func BuildTraf(spec FragmentSpec, dataOffset int32) []byte {
	tfhd := BuildFullBox("tfhd", 0, 0x020000, u32be(spec.TrackID)) // default-base-is-moof
	var tfdt []byte
	if spec.TfdtV0 {
		tfdt = BuildTfdtV0(uint32(spec.BaseMediaDecode))
	} else {
		tfdt = BuildTfdt(spec.BaseMediaDecode)
	}

	// BuildFullBox supplies the version/flags prefix, so the trun payload
	// starts directly with sample_count, then data-offset and entries.
	trunFlags := trunDataOffset | trunSampleDuration | trunSampleSize
	trunBody := make([]byte, 0, 4+4+8*len(spec.SampleSizes))
	trunBody = append(trunBody, u32be(uint32(len(spec.SampleSizes)))...)
	trunBody = append(trunBody, u32be(uint32(dataOffset))...)
	for i := range spec.SampleSizes {
		trunBody = append(trunBody, u32be(spec.SampleDurations[i])...)
		trunBody = append(trunBody, u32be(spec.SampleSizes[i])...)
	}
	trun := BuildFullBox("trun", 1, trunFlags, trunBody)
	return BuildBox("traf", append(tfhd, append(tfdt, trun...)...))
}

// BuildFragment assembles moof(mfhd,traf)+mdat and returns the complete
// chunk. It recomputes the trun data-offset so the output is self
// describing regardless of the caller's fragment order.
func BuildFragment(spec FragmentSpec) []byte {
	return buildFragment(spec, false)
}

// BuildFragmentNoMfhd emits a structurally valid fragment without an mfhd
// box, reproducing devices that omit sequence numbers.
func BuildFragmentNoMfhd(spec FragmentSpec) []byte {
	return buildFragment(spec, true)
}

func buildFragment(spec FragmentSpec, omitMfhd bool) []byte {
	// First pass with a placeholder offset to measure moof size.
	placeholder := BuildTraf(spec, 0)
	var moofInner []byte
	if !omitMfhd {
		moofInner = append(moofInner, BuildMfhd(spec.Seq)...)
	}
	moofInner = append(moofInner, placeholder...)
	moofSize := len(BuildBox("moof", moofInner))
	// data-offset is from the first byte of the mdat box header; the
	// payload begins after its 8-byte header.
	dataOffset := int32(moofSize) + 8
	traf := BuildTraf(spec, dataOffset)
	var inner []byte
	if !omitMfhd {
		inner = append(inner, BuildMfhd(spec.Seq)...)
	}
	inner = append(inner, traf...)
	moof := BuildBox("moof", inner)
	mdat := BuildBox("mdat", spec.SamplePayload)
	return append(moof, mdat...)
}
