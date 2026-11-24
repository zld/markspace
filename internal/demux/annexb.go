package demux

// annexBNALUs splits an Annex B byte stream into NAL units.
func annexBNALUs(b []byte) []annexBNAL {
	var out []annexBNAL
	start := -1
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				out = append(out, annexBNAL{off: start, data: trimZeros(b[start:i])})
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 && start < len(b) {
		out = append(out, annexBNAL{off: start, data: trimZeros(b[start:])})
	}
	return out
}

func trimZeros(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}
