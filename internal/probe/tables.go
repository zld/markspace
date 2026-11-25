package probe

import (
	"encoding/binary"

	"tracecheck/internal/clock"
)

// findChildPath descends a fixed box path from a node.
func findChildPath(root *Node, path ...string) *Node {
	cur := root
	for _, name := range path {
		cur = Find(cur.Children, name)
		if cur == nil {
			return nil
		}
	}
	return cur
}

func appendUnique(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

func maxLen(parts ...interface{}) int {
	n := 0
	for _, p := range parts {
		switch v := p.(type) {
		case []uint32:
			if len(v) > n {
				n = len(v)
			}
		case []int64:
			if len(v) > n {
				n = len(v)
			}
		}
	}
	return n
}

// fullBody returns the full-box body after the 4-byte version/flags header.
func fullBody(data []byte, n *Node) ([]byte, clock.Offset, error) {
	p, err := n.Payload(data)
	if err != nil {
		return nil, n.PayloadOff, err
	}
	if len(p) < 4 {
		return nil, n.PayloadOff, clock.NewOffsetError(n.PayloadOff,
			clock.CategoryTruncated, n.Type+" full-box header truncated", nil)
	}
	return p[4:], n.PayloadOff + 4, nil
}

func readStts(data []byte, stbl *Node, faults *[]*clock.OffsetError) []uint32 {
	n := Find(stbl.Children, "stts")
	if n == nil {
		*faults = append(*faults, clock.NewOffsetError(stbl.Offset,
			clock.CategoryMissingIndex, "stts absent", nil))
		return nil
	}
	body, off, err := fullBody(data, n)
	if err != nil {
		*faults = append(*faults, err.(*clock.OffsetError))
		return nil
	}
	if len(body) < 4 {
		*faults = append(*faults, clock.NewOffsetError(off,
			clock.CategoryTruncated, "stts entry count missing", nil))
		return nil
	}
	count := binary.BigEndian.Uint32(body[0:4])
	if err := guardCount(int64(count), "stts", off); err != nil {
		*faults = append(*faults, err.(*clock.OffsetError))
		return nil
	}
	var out []uint32
	pos := 4
	for e := uint32(0); e < count; e++ {
		if pos+8 > len(body) {
			*faults = append(*faults, clock.NewOffsetError(off+clock.Offset(pos),
				clock.CategoryTruncated, "stts entry truncated", nil))
			break
		}
		cnt := binary.BigEndian.Uint32(body[pos : pos+4])
		delta := binary.BigEndian.Uint32(body[pos+4 : pos+8])
		pos += 8
		for i := uint32(0); i < cnt; i++ {
			out = append(out, delta)
		}
	}
	return out
}

func readStsz(data []byte, stbl *Node, faults *[]*clock.OffsetError) []uint32 {
	n := Find(stbl.Children, "stsz")
	if n == nil {
		*faults = append(*faults, clock.NewOffsetError(stbl.Offset,
			clock.CategoryMissingIndex, "stsz absent", nil))
		return nil
	}
	body, off, err := fullBody(data, n)
	if err != nil {
		*faults = append(*faults, err.(*clock.OffsetError))
		return nil
	}
	if len(body) < 8 {
		*faults = append(*faults, clock.NewOffsetError(off,
			clock.CategoryTruncated, "stsz header truncated", nil))
		return nil
	}
	uniform := binary.BigEndian.Uint32(body[0:4])
	count := binary.BigEndian.Uint32(body[4:8])
	if err := guardCount(int64(count), "stsz", off); err != nil {
		*faults = append(*faults, err.(*clock.OffsetError))
		return nil
	}
	if uniform != 0 {
		out := make([]uint32, count)
		for i := range out {
			out[i] = uniform
		}
		return out
	}
	if len(body) < 8+int(count)*4 {
		*faults = append(*faults, clock.NewOffsetError(off+8,
			clock.CategoryTruncated, "stsz entry table truncated", nil))
		count = uint32((len(body) - 8) / 4)
	}
	out := make([]uint32, count)
	for i := range out {
		out[i] = binary.BigEndian.Uint32(body[8+i*4 : 12+i*4])
	}
	return out
}

// readOffsets expands stco/co64 chunk offsets using one-sample-per-chunk stsc,
// which is the layout the fixture writer emits.
func readOffsets(data []byte, stbl *Node, faults *[]*clock.OffsetError, tr *Track) []int64 {
	n := Find(stbl.Children, "stco")
	if n == nil {
		n = Find(stbl.Children, "co64")
	}
	if n == nil {
		*faults = append(*faults, clock.NewOffsetError(stbl.Offset,
			clock.CategoryMissingIndex, "stco/co64 absent", nil))
		return nil
	}
	body, off, err := fullBody(data, n)
	if err != nil {
		*faults = append(*faults, err.(*clock.OffsetError))
		return nil
	}
	if len(body) < 4 {
		*faults = append(*faults, clock.NewOffsetError(off,
			clock.CategoryTruncated, n.Type+" count missing", nil))
		return nil
	}
	count := binary.BigEndian.Uint32(body[0:4])
	if err := guardCount(int64(count), n.Type, off); err != nil {
		*faults = append(*faults, err.(*clock.OffsetError))
		return nil
	}
	width := 4
	if n.Type == "co64" {
		width = 8
	}
	if len(body) < 4+int(count)*width {
		*faults = append(*faults, clock.NewOffsetError(off+4,
			clock.CategoryTruncated, n.Type+" offset table truncated", nil))
		count = uint32((len(body) - 4) / width)
	}
	out := make([]int64, count)
	for i := range out {
		if width == 8 {
			out[i] = int64(binary.BigEndian.Uint64(body[4+i*8:]))
		} else {
			out[i] = int64(binary.BigEndian.Uint32(body[4+i*4:]))
		}
	}
	return out
}

// readStss returns a per-sample boolean slice. With no stss box every sample
// is treated as a sync sample (the ISO rule).
func readStss(data []byte, stbl *Node, faults *[]*clock.OffsetError) []bool {
	n := Find(stbl.Children, "stss")
	if n == nil {
		return nil
	}
	body, off, err := fullBody(data, n)
	if err != nil {
		*faults = append(*faults, err.(*clock.OffsetError))
		return nil
	}
	if len(body) < 4 {
		*faults = append(*faults, clock.NewOffsetError(off,
			clock.CategoryTruncated, "stss count missing", nil))
		return nil
	}
	count := binary.BigEndian.Uint32(body[0:4])
	if err := guardCount(int64(count), "stss", off); err != nil {
		*faults = append(*faults, err.(*clock.OffsetError))
		return nil
	}
	sync := make(map[uint32]bool, count)
	for i := uint32(0); i < count; i++ {
		entry := 4 + int(i)*4
		if entry+4 > len(body) {
			*faults = append(*faults, clock.NewOffsetError(off+clock.Offset(entry),
				clock.CategoryTruncated, "stss entry truncated", nil))
			break
		}
		sync[binary.BigEndian.Uint32(body[entry:entry+4])] = true
	}
	// Highest referenced sample number bounds the returned boolean slice.
	max := uint32(0)
	for num := range sync {
		if num > max {
			max = num
		}
	}
	out := make([]bool, max)
	for num := range sync {
		out[num-1] = true
	}
	return out
}
