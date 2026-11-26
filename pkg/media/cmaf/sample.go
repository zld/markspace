package cmaf

// Sample is one trun entry plus its computed timeline position.
type Sample struct {
	Index      uint32 // 0-based within the fragment
	Duration   uint32 // trun sample duration, or tfhd/trex default
	Size       uint32 // trun sample size, or tfhd/trex default
	Flags      uint32
	Sync       bool   // sample is a random-access point
	MediaTime  uint64 // absolute media time of the sample (track timescale)
	PayloadOff int64  // absolute mdat payload offset of sample bytes
	PayloadLen int64
}

// isSyncSample applies the ISO BMFF sample-depends-on rule: bits 24-25
// value 2 means the sample does not depend on others (a sync sample).
func isSyncSample(flags uint32) bool {
	return (flags>>24)&0x03 == 0x02
}
