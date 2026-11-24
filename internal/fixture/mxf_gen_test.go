package fixture

import (
	"encoding/binary"
	"testing"
)

func TestBERLengthRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 300, 65535, 1 << 20} {
		enc := berLen(n)
		got, consumed, ok := decodeBER(enc)
		if !ok || got != n || consumed != len(enc) {
			t.Fatalf("BER round trip %d -> %v -> %d (consumed %d ok %v)", n, enc, got, consumed, ok)
		}
	}
}

func TestGenerateMXFStructure(t *testing.T) {
	spec := DefaultMXFSpec(7)
	data, manifest := GenerateMXF(spec)

	// Walk top-level KLV triplets and record keys.
	var keys [][16]byte
	pos := 0
	for pos < len(data) {
		if pos+16 > len(data) {
			t.Fatal("truncated KLV key")
		}
		var key [16]byte
		copy(key[:], data[pos:pos+16])
		keys = append(keys, key)
		n, consumed, ok := decodeBER(data[pos+16:])
		if !ok {
			t.Fatalf("bad BER at offset %d", pos)
		}
		pos += 16 + consumed + n
	}
	if pos != len(data) {
		t.Fatalf("KLV walk ended at %d, file length %d", pos, len(data))
	}
	want := [][16]byte{mxfKeyHeader, mxfKeyPreface, mxfKeyPreface, mxfKeyBody, mxfKeyFooter}
	if len(keys) != len(want) {
		t.Fatalf("top-level KLV count = %d, want %d", len(keys), len(want))
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("KLV %d key mismatch", i)
		}
	}

	// Essence offset points inside the body value.
	if manifest.EssenceOffset <= 0 || manifest.EssenceOffset >= int64(len(data)) {
		t.Fatalf("essence offset %d outside file of %d bytes", manifest.EssenceOffset, len(data))
	}
	if manifest.EditRateNum != 30000 || manifest.EditRateDen != 1001 {
		t.Fatal("edit rate manifest mismatch")
	}
}

func TestGenerateMXFDeterministicAndDropIndex(t *testing.T) {
	a, _ := GenerateMXF(DefaultMXFSpec(7))
	b, _ := GenerateMXF(DefaultMXFSpec(7))
	if string(a) != string(b) {
		t.Fatal("MXF generation not deterministic")
	}
	spec := DefaultMXFSpec(7)
	spec.DropIndex = true
	dropped, _ := GenerateMXF(spec)
	if findLocalTag(dropped, mxfTagIndexTable) >= 0 {
		t.Fatal("index table present despite DropIndex")
	}
}

// decodeBER parses a SMPTE BER length; decodeBER is the parser counterpart
// used by the container subsystem and the tests.
func decodeBER(b []byte) (value int, consumed int, ok bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	if b[0]&0x80 == 0 {
		return int(b[0]), 1, true
	}
	n := int(b[0] & 0x7F)
	if n == 0 || n > 8 || 1+n > len(b) {
		return 0, 0, false
	}
	v := 0
	for i := 1; i <= n; i++ {
		v = v<<8 | int(b[i])
	}
	return v, 1 + n, true
}

// findLocalTag scans the file for a preface local item with the tag.
func findLocalTag(data []byte, tag byte) int {
	pos := 0
	for pos+16 <= len(data) {
		if pos+16 >= len(data) {
			return -1
		}
		n, consumed, ok := decodeBER(data[pos+16:])
		if !ok || pos+16+consumed+n > len(data) {
			return -1
		}
		valueStart := pos + 16 + consumed
		var key [16]byte
		copy(key[:], data[pos:pos+16])
		if key == mxfKeyPreface {
			if off := scanItems(data[valueStart:valueStart+n], tag); off >= 0 {
				return valueStart + off
			}
		}
		pos = valueStart + n
	}
	return -1
}

func scanItems(b []byte, tag byte) int {
	p := 0
	for p+1 < len(b) {
		if b[p] == tag {
			return p
		}
		n, consumed, ok := decodeBER(b[p+1:])
		if !ok {
			return -1
		}
		p += 1 + consumed + n
	}
	return -1
}

var _ = binary.BigEndian
