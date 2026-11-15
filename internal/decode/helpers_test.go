package decode

import (
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func utf16LEWithBOM() encoding.Encoding {
	return unicode.UTF16(unicode.LittleEndian, unicode.UseBOM)
}

func utf16BEWithBOM() encoding.Encoding {
	return unicode.UTF16(unicode.BigEndian, unicode.UseBOM)
}

func mustEncode(t *testing.T, enc encoding.Encoding, s string) []byte {
	t.Helper()
	out, _, err := transform.String(enc.NewEncoder(), s)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return []byte(out)
}
