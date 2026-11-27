// Package klv defines the on-disk constants of the minimal MXF KLV subset
// shared by the fixture writer and the demux reader, so the two sides can
// never drift apart.
package klv

// 16-byte UL keys. The first 12 bytes are a fixed registered-style prefix;
// the final byte identifies the local set.
var (
	prefix = []byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01,
		0x0d, 0x01, 0x02, 0x00}
	// KeyHeaderPack opens the MXF image.
	KeyHeaderPack = Key(0x01)
	// KeyPreface carries one track's descriptive metadata.
	KeyPreface = Key(0x02)
	// KeyTrack is a single-row sample index table.
	KeyTrack = Key(0x03)
	// KeyEssence wraps one compressed sample's bytes.
	KeyEssence = Key(0x04)
)

// HeaderMagic is the 16-byte marker inside the header partition pack.
const HeaderMagic = "MXF-HDR-PACK-v01"

// Key builds a UL key for the given local tag byte.
func Key(tag byte) [16]byte {
	var k [16]byte
	copy(k[:], prefix)
	k[15] = tag
	return k
}

// Preface field offsets are documented here as the canonical layout:
//
//	timescale  u32 | trackID u32 | codec u8str | handler u8str
//	rotation   u32 | cropL/cropT/cropR/cropB u32 each
//	primaries  u16 | transfer u16 | matrix u16 | fullRange u8
//	rtpBase    u32 | rtpModulus u32
//
// Index table (KeyTrack) value is exactly 40 bytes:
//
//	trackID u32 | index u32 | dts u64 | duration u64
//	offset  u64 | size  u32 | rtpRaw u32
const (
	IndexRowSize   = 40
	HeaderPackSize = 20 // magic(16) + trackCount(4)
	EssenceHdrMin  = 17 // key(16) + 1-byte short-form BER
)
