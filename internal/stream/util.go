package stream

import "encoding/hex"

func hex32(b []byte) string {
	if len(b) != 32 {
		return ""
	}
	return hex.EncodeToString(b)
}
