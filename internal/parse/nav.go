package parse

import "encoding/binary"

// childrenOf returns the direct child boxes of b matching typ.
func childrenOf(b BoxRecord, typ string) []BoxRecord {
	var out []BoxRecord
	for _, ch := range b.Children {
		if ch.Type == typ {
			out = append(out, ch)
		}
	}
	return out
}

// childOf returns the first matching direct child.
func childOf(b BoxRecord, typ string) (BoxRecord, bool) {
	for _, ch := range b.Children {
		if ch.Type == typ {
			return ch, true
		}
	}
	return BoxRecord{}, false
}

// findNestedBox walks a fixed path of nested child box names.
func findNestedBox(root BoxRecord, path []string) (BoxRecord, bool) {
	cur := root
	for _, name := range path {
		next, ok := childOf(cur, name)
		if !ok {
			return BoxRecord{}, false
		}
		cur = next
	}
	return cur, true
}

// findTopLevel locates a top-level box by type.
func findTopLevel(c *Container, typ string) (BoxRecord, bool) {
	for _, b := range c.Boxes {
		if b.Type == typ {
			return b, true
		}
	}
	return BoxRecord{}, false
}

// readTrackID extracts the track_id from a trak's tkhd box.
func readTrackID(data []byte, trak BoxRecord) int {
	tkhd, ok := childOf(trak, "tkhd")
	if !ok {
		return -1
	}
	p := tkhd.Offset + tkhd.HeaderSize + 4 + 8 // version/flags + create + modify
	return int(binary.BigEndian.Uint32(data[p:]))
}

// readHandlerType extracts handler_type from an hdlr box.
func readHandlerType(data []byte, hdlr BoxRecord) string {
	p := hdlr.Offset + hdlr.HeaderSize + 8 // FullBox(4) + pre_defined(4)
	if p+4 > hdlr.Offset+hdlr.Size {
		return "????"
	}
	return string(data[p : p+4])
}
