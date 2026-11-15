package demo

import (
	"bytes"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// EncodeUTF16LE re-encodes UTF-8 text as little-endian UTF-16 and prefixes
// the standard BOM (FF FE), matching the acceptance demo requirement.
func EncodeUTF16LE(utf8Text string) ([]byte, error) {
	enc := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM)
	var buf bytes.Buffer
	w := transform.NewWriter(&buf, enc.NewEncoder())
	if _, err := w.Write([]byte(utf8Text)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
