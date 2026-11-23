// Package mxfkv defines the restricted SMPTE-style KLV vocabulary understood
// by both the MXF fixture writer and the MXF ingest parser. Keys outside this
// vocabulary are reported as unknown structures with their byte offset.
package mxfkv

// BER long-form length prefix marker.
const BERLong = 0x80

// KeyLen is the SMPTE UL key length.
const KeyLen = 16

// Local tags used inside declared local-set values.
const (
	TagEssenceUL    = 0x01
	TagBodySID      = 0x02
	TagTrackKind    = 0x10
	TagTimescale    = 0x11
	TagSampleCount  = 0x12
	TagRotation     = 0x13
	TagCrop         = 0x14
	TagColor        = 0x15
	TagIndexCount   = 0x20
	TagIndexRecords = 0x21
)

// Track kind values.
const (
	KindAudio = 1
	KindVideo = 2
)

// IndexRecSize is bytes per timeline index record:
// PTS u64, duration u32, payload length u32, flags u8, keyframe flag u8.
const IndexRecSize = 8 + 4 + 4 + 1 + 1

var prefix = []byte{0x06, 0x0e, 0x2b, 0x34}

// KeyHeaderPartition identifies the header partition pack.
func KeyHeaderPartition() [16]byte {
	return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01,
		0x0d, 0x01, 0x02, 0x01, 0x01, 0x02, 0x00, 0x00}
}

// KeyPreface identifies the preface/identification set.
func KeyPreface() [16]byte {
	return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01,
		0x0d, 0x01, 0x02, 0x01, 0x01, 0x00, 0x00, 0x00}
}

// KeyTrack identifies a track metadata local set.
func KeyTrack() [16]byte {
	return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01,
		0x0d, 0x01, 0x03, 0x01, 0x02, 0x00, 0x00, 0x00}
}

// KeyTimelineIndex identifies the per-sample timeline index set.
func KeyTimelineIndex() [16]byte {
	return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01,
		0x0d, 0x01, 0x03, 0x01, 0x03, 0x00, 0x00, 0x00}
}

// KeyEssence identifies an essence container data item (one sample each).
func KeyEssence() [16]byte {
	return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01,
		0x0d, 0x01, 0x03, 0x01, 0x05, 0x00, 0x00, 0x00}
}

// KeyUnknown is deliberately outside the vocabulary; parsers must preserve
// its offset and report it rather than fail the whole file.
func KeyUnknown() [16]byte {
	return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x7f, 0x7f, 0x7f, 0x7f,
		0x0d, 0x01, 0x99, 0x01, 0x99, 0x00, 0x00, 0x00}
}

// IsKnown reports whether key belongs to the declared vocabulary.
func IsKnown(key [16]byte) bool {
	for _, k := range [][16]byte{
		KeyHeaderPartition(), KeyPreface(), KeyTrack(),
		KeyTimelineIndex(), KeyEssence(),
	} {
		if k == key {
			return true
		}
	}
	return false
}

// IsPrefix sanity-checks the SMPTE UL prefix.
func IsPrefix(key [16]byte) bool {
	for i := range prefix {
		if key[i] != prefix[i] {
			return false
		}
	}
	return true
}
