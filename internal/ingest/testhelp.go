package ingest

// Test-only constructors shared across package tests. Kept in a _test-free
// helper so external package tests (index, verify) can build stamped records
// without re-deriving the private header layout.

// NewRecordForTest wraps a fully formed 188-byte packet as a stamped record.
func NewRecordForTest(raw [PacketSize]byte, index, ticks int64) *Record {
	p := parseHeader(0, index, &raw)
	rec := Record{Packet: p, Ticks: ticks, HasTime: ticks != 0}
	return &rec
}

// CodecH264ForTest returns the H.264 codec tag for test mappings.
func CodecH264ForTest() string { return "h264" }

// CodecHEVCForTest returns the HEVC codec tag for test mappings.
func CodecHEVCForTest() string { return "hevc" }
