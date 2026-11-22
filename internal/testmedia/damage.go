package testmedia

import "mediasync/internal/ebml"

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Damage labels every mutation the fixture factory can apply. Tests assert
// the specific failure class each label must produce.
type Damage string

const (
	DamageTruncate       Damage = "truncate"        // cut bytes inside the final cluster
	DamageUnknownSegment Damage = "unknown-segment" // Segment encoded unknown-length
	DamageUnknownCluster Damage = "unknown-cluster" // Clusters encoded unknown-length
	DamageBadVINT        Damage = "bad-vint"        // corrupt a size VINT
)

// ApplyDamage returns a mutated copy of a generated container. The original
// slice is never modified, so the healthy fixture remains available for
// cross-checking invariants.
func ApplyDamage(data []byte, dmg Damage) []byte {
	switch dmg {
	case DamageTruncate:
		cut := len(data) * 3 / 4
		if cut >= len(data) {
			cut = len(data) - 1
		}
		return append([]byte(nil), data[:cut]...)
	case DamageUnknownSegment:
		return flipSegmentUnknown(data)
	case DamageUnknownCluster:
		return flipClustersUnknown(data)
	case DamageBadVINT:
		out := append([]byte(nil), data...)
		// Corrupt the size VINT of the first Cluster by setting an
		// impossible width marker (leading zero byte never starts a VINT).
		idx := locateFirstCluster(out)
		if idx < 0 || idx+4 >= len(out) {
			return out
		}
		out[idx+4] = 0x00
		return out
	default:
		return append([]byte(nil), data...)
	}
}

// flipSegmentUnknown rewrites the Segment header to use the 1-byte unknown
// length marker and shifts nothing else: the remainder of the file stays
// byte-identical, only the (smaller) size VINT is replaced, so one padding
// byte is removed to keep offsets coherent.
func flipSegmentUnknown(data []byte) []byte {
	idx := locateSegment(data)
	if idx < 0 {
		return append([]byte(nil), data...)
	}
	idLen := len(ebml.EncodeID(ebml.IDSegment))
	sizeStart := idx + idLen
	width := vintWidth(data[sizeStart])
	out := make([]byte, 0, len(data)-width+1)
	out = append(out, data[:sizeStart]...)
	out = append(out, ebml.EncodeUnknownSize()...)
	out = append(out, data[sizeStart+width:]...)
	return out
}

// flipClustersUnknown rewrites every Cluster header. All header offsets are
// collected against the original bytes first, then flips are applied from the
// last offset backwards so earlier offsets stay valid as the slice shrinks.
func flipClustersUnknown(data []byte) []byte {
	id := ebml.EncodeID(ebml.IDCluster)
	idLen := len(id)
	var sizeStarts []int
	for i := 0; i+idLen < len(data); i++ {
		if !bytesEqual(data[i:i+idLen], id) {
			continue
		}
		// A real Cluster header is followed by a valid size VINT whose marker
		// bit is set; codec payload may contain the same four bytes but will
		// not be followed by a well-formed size VINT.
		sizeStart := i + idLen
		if sizeStart >= len(data) || data[sizeStart]&0x80 == 0 {
			continue
		}
		sizeStarts = append(sizeStarts, sizeStart)
	}
	out := append([]byte(nil), data...)
	for k := len(sizeStarts) - 1; k >= 0; k-- {
		sizeStart := sizeStarts[k]
		width := vintWidth(out[sizeStart])
		rewritten := make([]byte, 0, len(out)-width+1)
		rewritten = append(rewritten, out[:sizeStart]...)
		rewritten = append(rewritten, ebml.EncodeUnknownSize()...)
		rewritten = append(rewritten, out[sizeStart+width:]...)
		out = rewritten
	}
	return out
}

func locateSegment(data []byte) int {
	return locateID(data, ebml.EncodeID(ebml.IDSegment), 0)
}

func locateFirstCluster(data []byte) int {
	return locateID(data, ebml.EncodeID(ebml.IDCluster), 0)
}

// locateID finds the first occurrence of an EBML element ID's exact byte
// sequence at or after offset from. IDs may share leading bytes with other
// element data, so callers that flip headers run on freshly serialized
// fixtures where the sought ID occurs only as a real element header.
func locateID(data, id []byte, from int) int {
	for i := from; i+len(id) <= len(data); i++ {
		match := true
		for j := range id {
			if data[i+j] != id[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// vintWidth decodes the VINT width from its first marker byte.
func vintWidth(b byte) int {
	w := 1
	for mask := byte(0x80); b&mask == 0; mask >>= 1 {
		w++
	}
	return w
}
