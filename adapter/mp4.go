package adapter

// MP4Adapter parses ISOBMFF containers through go-mp4. The concrete parser is
// implemented in mp4_parse.go; this file holds the versioned descriptor.
type MP4Adapter struct {
	baseAdapter
}

// MP4AdapterName is the registry key for the MP4 parser family.
const MP4AdapterName = "mp4"

// MP4AdapterV1 is the first (and current) parser version.
const MP4AdapterV1 uint32 = 1

// NewMP4AdapterV1 builds the version-1 MP4 adapter descriptor.
func NewMP4AdapterV1() *MP4Adapter {
	return &MP4Adapter{baseAdapter{
		name: MP4AdapterName, ver: MP4AdapterV1,
		caps: map[Capability]struct{}{
			CapEditList:    {},
			CapSparseKey:   {},
			CapNonZeroPTS:  {},
			CapVariableFPS: {},
		},
	}}
}
