package fixture

import (
	"encoding/binary"
	"fmt"
)

// MXF fixture writer for the KLV profile accepted by internal/track: a header
// partition, primer pack, package/metadata sets, and essence elements whose key
// suffix is the timeline track number.

var (
	ulPartition  = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01, 0x02, 0x04, 0x00, 0x00}
	ulPrimer     = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01, 0x01, 0x05, 0x01, 0x00}
	ulEssencePic = []byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01, 0x0d, 0x01, 0x03, 0x01}
	ulEssenceSnd = []byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01, 0x0d, 0x01, 0x03, 0x01}
	ulSetBase    = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01}
)

func setKey(kind byte) []byte {
	// A 16-byte set key is the 14-byte class/version prefix, the registry
	// kind and a trailing 0x00 reserved byte.
	k := append([]byte(nil), ulSetBase...)
	return append(k, kind, 0x00)
}

func essenceKey(prefix []byte, track uint32) []byte {
	k := append([]byte(nil), prefix...)
	return binary.BigEndian.AppendUint32(k, track)
}

// berLen encodes a BER length.
func berLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var tmp []byte
	for v := n; v > 0; v >>= 8 {
		tmp = append([]byte{byte(v)}, tmp...)
	}
	out := []byte{0x80 | byte(len(tmp))}
	return append(out, tmp...)
}

func klv(key, value []byte) []byte {
	out := append([]byte(nil), key...)
	out = append(out, berLen(len(value))...)
	return append(out, value...)
}

// localItem encodes one local-set item with an explicit tag ordering key.
type localItem struct {
	tag uint16
	val []byte
}

func localSet(kind byte, items ...localItem) []byte {
	var body []byte
	for _, it := range items {
		body = append(body, u16(it.tag)...)
		body = append(body, berLen(len(it.val))...)
		body = append(body, it.val...)
	}
	// KLV set keys end with 0x00; the local-set kind occupies the byte before.
	return klv(setKey(kind), body)
}

func ul(seed byte) []byte {
	// InstanceUIDs are 16-byte UUIDs; the fixture writes deterministic
	// values in the private range so every run produces identical bytes.
	out := make([]byte, 16)
	out[0], out[1], out[2], out[3] = 0x06, 0x0e, 0x2b, 0x34
	out[4], out[5] = 0x02, 0x53
	for i := 6; i < 15; i++ {
		out[i] = seed ^ byte(i*7)
	}
	out[15] = seed
	return out
}

// mxfSpec controls the generated MXF metadata.
type mxfSpec struct {
	pictureFrames  int
	picturePayload int
	audioFrames    int
	audioPayload   int
	editRateNum    int32
	editRateDen    int32
	pictureColors  [3]uint16
	// extraColors adds an orphan picture descriptor carrying a conflicting
	// color description for the same track.
	extraColors []uint16
	damage      bool
}

func (s mxfSpec) build() ([]byte, int64) {
	var body []byte
	body = append(body, klv(ulPartition, partitionValue())...)

	// Primer map: every local tag used below.
	tags := []uint16{
		0x0201, 0x0202, 0x3c0a, 0x3006, 0x3202, 0x3203, 0x3204, 0x3205,
		0x3206, 0x3207, 0x3208, 0x3209, 0x320a, 0x320b, 0x3219, 0x321a,
		0x321b, 0x4801, 0x4803, 0x4804, 0x4b02, 0x4b05, 0x3d01, 0x3d03,
		0x3d07, 0x3d0a,
	}
	primer := u32(uint32(len(tags)))
	primer = append(primer, u32(18)...)
	for _, t := range tags {
		primer = append(primer, u16(t)...)
		primer = append(primer, ul(byte(t))...)
	}
	body = append(body, klv(ulPrimer, primer)...)

	duration := u32(uint32(s.pictureFrames))
	body = append(body, localSet(0x2f,
		localItem{0x3c0a, ul(0x2f)},
		localItem{0x0202, duration},
	)...)
	body = append(body, localSet(0x18,
		localItem{0x3c0a, ul(0x18)},
	)...)
	body = append(body, localSet(0x36,
		localItem{0x3c0a, ul(0x36)},
		localItem{0x0202, duration},
	)...)
	body = append(body, localSet(0x37,
		localItem{0x3c0a, ul(0x37)},
	)...)
	body = append(body, localSet(0x3b,
		localItem{0x3c0a, ul(0x3b)},
		localItem{0x4801, u32(1)},
		localItem{0x4804, []byte("Picture")},
		localItem{0x4b05, editRate(s)},
		localItem{0x0202, duration},
	)...)
	body = append(body, localSet(0x0f,
		localItem{0x0202, duration},
		localItem{0x4b05, editRate(s)},
	)...)
	body = append(body, localSet(0x11,
		localItem{0x0202, duration},
		localItem{0x4b02, u64(0)},
		localItem{0x4803, ul(0x37)},
	)...)
	body = append(body, localSet(0x3b,
		localItem{0x3c0a, ul(0x3b + 1)},
		localItem{0x4801, u32(2)},
		localItem{0x4804, []byte("Sound")},
		localItem{0x4b05, editRate(s)},
		localItem{0x0202, duration},
	)...)
	body = append(body, pictureDescriptor(s, 0x28, s.pictureColors, 0x28)...)
	for i, c := range s.extraColors {
		_ = i
		body = append(body, pictureDescriptor(s, 0x28, [3]uint16{c, s.pictureColors[1], s.pictureColors[2]}, 0x29)...)
	}
	body = append(body, localSet(0x48,
		localItem{0x3c0a, ul(0x48)},
		uint32Item(0x3006, 2),
		localItem{0x3d03, editRate(s)},
		uint32Item(0x3d07, 2),
		uint32Item(0x3d01, 16),
		uint32Item(0x3d0a, 4),
	)...)

	damageAt := int64(-1)
	if s.damage {
		// Truncate inside the last picture essence element by declaring a
		// length larger than the remaining file.
		key := essenceKey(ulEssencePic, 1)
		frame := make([]byte, s.picturePayload)
		for i := range frame {
			frame[i] = byte(i * 5)
		}
		good := klv(key, frame)
		body = append(body, good...)
		bad := append([]byte(nil), key...)
		bad = append(bad, berLen(len(frame))...)
		bad = append(bad, frame[:len(frame)/2]...)
		damageAt = int64(len(partitionValue()) + 16 + 2) // recomputed by caller
		body = append(body, bad...)
		return body, damageAt
	}

	for i := 0; i < s.pictureFrames; i++ {
		frame := make([]byte, s.picturePayload)
		for k := range frame {
			frame[k] = byte(i) ^ byte(k*5)
		}
		body = append(body, klv(essenceKey(ulEssencePic, 1), frame)...)
	}
	for i := 0; i < s.audioFrames; i++ {
		frame := make([]byte, s.audioPayload)
		for k := 0; k < len(frame); k += 4 {
			amp := int16(0)
			ms := int64(i) * 40
			if ms >= 800 {
				amp = 1500
			}
			binary.LittleEndian.PutUint16(frame[k:], uint16(amp))
			binary.LittleEndian.PutUint16(frame[k+2:], uint16(-amp))
		}
		body = append(body, klv(essenceKey(ulEssenceSnd, 2), frame)...)
	}
	return body, -1
}

// colorUL encodes a color property enumeration as a 16-byte UL.
func colorUL(v uint16) []byte {
	b := []byte{0x06, 0x0e, 0x2b, 0x34, 0x04, 0x01, 0x01, 0x01, 0x04, 0x01, 0x01, 0x01, 0x01, 0x02, 0x00, 0x00}
	binary.BigEndian.PutUint16(b[14:], v)
	return b
}

func editRate(s mxfSpec) []byte {
	return cat(i32(s.editRateNum), i32(s.editRateDen))
}

func uint32Item(tag uint16, v uint32) localItem {
	return localItem{tag: tag, val: u32(v)}
}

func pictureDescriptor(s mxfSpec, kind byte, colors [3]uint16, uidSeed byte) []byte {
	w := uint32(1920)
	h := uint32(1080)
	// Color properties are UL enumerations: 16-byte keys in the SMPTE
	// RP 210 table. The fixture writes the plain UL value so parsers that
	// resolve the enumeration and parsers that keep the raw code agree.
	return localSet(kind,
		localItem{0x3c0a, ul(uidSeed)},
		uint32Item(0x3006, 1),
		uint32Item(0x3203, w),
		uint32Item(0x3202, h),
		uint32Item(0x3205, w),
		uint32Item(0x3204, h),
		uint32Item(0x3209, w-16),
		uint32Item(0x3208, h),
		uint32Item(0x320a, 8),
		uint32Item(0x320b, 0),
		localItem{0x3219, colorUL(colors[0])},
		localItem{0x321a, colorUL(colors[1])},
		localItem{0x321b, colorUL(colors[2])},
	)
}

func partitionValue() []byte {
	v := u16(1)
	v = append(v, u16(3)...)
	v = append(v, u32(0)...)                                                 // KAG size
	v = append(v, u64(0)...)                                                 // this partition
	v = append(v, u64(0)...)                                                 // previous
	v = append(v, u64(0)...)                                                 // footer
	v = append(v, []byte{0x0d, 0x01, 0x02, 0x01, 0x01, 0x02, 0x04, 0x00}...) // OP1a
	for i := 0; i < 16; i++ {
		v = append(v, 0)
	}
	return v
}

var pictureFramesDefault = 60

// buildMXF renders a valid or conflicting two-track MXF.
func buildMXF(conflict, _ bool) ([]byte, error) {
	s := mxfSpec{
		pictureFrames:  pictureFramesDefault,
		picturePayload: 384,
		audioFrames:    60,
		audioPayload:   128,
		editRateNum:    25,
		editRateDen:    1,
		pictureColors:  [3]uint16{1, 1, 1},
	}
	if conflict {
		s.extraColors = []uint16{9}
	}
	body, _ := s.build()
	out := body
	if len(out) == 0 {
		return nil, fmt.Errorf("empty mxf")
	}
	return out, nil
}

// buildDamagedMXF truncates an otherwise valid MXF inside a picture essence
// element and returns the first missing byte offset plus a small slack factor
// used by tests (they allow +/-1 byte).
func buildDamagedMXF() ([]byte, int64, error) {
	s := mxfSpec{
		pictureFrames:  6,
		picturePayload: 384,
		audioFrames:    6,
		audioPayload:   128,
		editRateNum:    25,
		editRateDen:    1,
		pictureColors:  [3]uint16{1, 1, 1},
	}
	full, _ := s.build()
	cut := len(full) - 64
	out := append([]byte(nil), full[:cut]...)
	return out, int64(cut), nil
}
