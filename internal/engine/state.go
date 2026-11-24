package engine

// atou parses a decimal track key, rejecting anything else so a damaged
// checkpoint cannot silently map onto the wrong track.
func atou(s string) (uint32, bool) {
	if s == "" || len(s) > 10 {
		return 0, false
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		v = v*10 + uint64(s[i]-'0')
	}
	return uint32(v), true
}

// itoa renders a track id as a decimal key.
func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
