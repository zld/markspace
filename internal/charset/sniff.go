// Package charset detects and decodes subtitle file encodings.
package charset

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	xencoding "golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

// Encoding names a decoded or user-requested character encoding.
type Encoding string

const (
	UTF8        Encoding = "utf-8"
	UTF8BOM     Encoding = "utf-8-bom"
	UTF16LE     Encoding = "utf-16le"
	UTF16BE     Encoding = "utf-16be"
	UTF16LEBOM  Encoding = "utf-16le-bom"
	UTF16BEBOM  Encoding = "utf-16be-bom"
	GB18030     Encoding = "gb18030"
	GBK         Encoding = "gbk"
	Windows1252 Encoding = "windows-1252"
)

// Error reports an undecodable or undetectable byte sequence.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

var (
	bomUTF8   = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// Supported reports whether name is a valid explicit --encoding value.
func Supported(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "utf-8", "utf8", "utf-16", "utf-16le", "utf-16be",
		"gb18030", "gbk", "windows-1252", "cp1252":
		return true
	}
	return false
}

// Decode converts raw file bytes to UTF-8 text. When explicit is empty the
// encoding is sniffed from BOMs and byte patterns; otherwise it must be one
// of the names accepted by Supported.
func Decode(data []byte, explicit string) (string, Encoding, error) {
	if explicit != "" {
		return decodeExplicit(data, explicit)
	}
	return sniff(data)
}

func sniff(data []byte) (string, Encoding, error) {
	switch {
	case bytes.HasPrefix(data, bomUTF8):
		body := data[len(bomUTF8):]
		if !utf8.Valid(body) {
			return "", "", &Error{Msg: "utf-8 BOM present but body is not valid utf-8"}
		}
		return string(body), UTF8BOM, nil
	case bytes.HasPrefix(data, bomUTF16LE):
		return decodeUTF16(data[len(bomUTF16LE):], unicode.LittleEndian, UTF16LEBOM)
	case bytes.HasPrefix(data, bomUTF16BE):
		return decodeUTF16(data[len(bomUTF16BE):], unicode.BigEndian, UTF16BEBOM)
	}
	if enc, ok := guessUTF16(data); ok {
		endian := unicode.LittleEndian
		if enc == UTF16BE {
			endian = unicode.BigEndian
		}
		return decodeUTF16(data, endian, enc)
	}
	if utf8.Valid(data) {
		return string(data), UTF8, nil
	}
	return "", "", &Error{Msg: "cannot determine encoding: no BOM, not utf-16 pattern, not valid utf-8; pass --encoding explicitly"}
}

// guessUTF16 detects BOM-less UTF-16 by the NUL-byte distribution of
// predominantly ASCII text.
func guessUTF16(data []byte) (Encoding, bool) {
	n := len(data)
	if n > 512 {
		n = 512
	}
	n -= n % 2
	if n < 4 {
		return "", false
	}
	var evenNUL, oddNUL int
	for i := 0; i < n; i += 2 {
		if data[i] == 0 {
			evenNUL++
		}
		if data[i+1] == 0 {
			oddNUL++
		}
	}
	pairs := n / 2
	switch {
	case oddNUL*4 > pairs*3 && evenNUL == 0:
		return UTF16LE, true
	case evenNUL*4 > pairs*3 && oddNUL == 0:
		return UTF16BE, true
	}
	return "", false
}

func decodeExplicit(data []byte, name string) (string, Encoding, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "utf-8", "utf8":
		body := bytes.TrimPrefix(data, bomUTF8)
		if !utf8.Valid(body) {
			return "", "", &Error{Msg: "input is not valid utf-8"}
		}
		return string(body), UTF8, nil
	case "utf-16", "utf-16le", "utf-16be":
		endian := unicode.LittleEndian
		enc := UTF16LE
		if strings.HasSuffix(name, "be") {
			endian = unicode.BigEndian
			enc = UTF16BE
		}
		body := data
		if bytes.HasPrefix(body, bomUTF16LE) || bytes.HasPrefix(body, bomUTF16BE) {
			body = body[2:]
		}
		return decodeUTF16(body, endian, enc)
	case "gb18030":
		return transformDecode(data, simplifiedchinese.GB18030, GB18030)
	case "gbk":
		return transformDecode(data, simplifiedchinese.GBK, GBK)
	case "windows-1252", "cp1252":
		return transformDecode(data, charmap.Windows1252, Windows1252)
	}
	return "", "", &Error{Msg: fmt.Sprintf("unsupported encoding %q", name)}
}

func decodeUTF16(body []byte, endian unicode.Endianness, enc Encoding) (string, Encoding, error) {
	if len(body)%2 != 0 {
		return "", "", &Error{Msg: fmt.Sprintf("%s: odd byte count %d", enc, len(body))}
	}
	return transformDecode(body, unicode.UTF16(endian, unicode.IgnoreBOM), enc)
}

func transformDecode(data []byte, e xencoding.Encoding, enc Encoding) (string, Encoding, error) {
	s, err := e.NewDecoder().String(string(data))
	if err != nil {
		return "", "", &Error{Msg: fmt.Sprintf("%s decode failed: %v", enc, err)}
	}
	if strings.ContainsRune(s, '�') {
		return "", "", &Error{Msg: fmt.Sprintf("%s: input contains byte sequences that cannot be decoded", enc)}
	}
	return s, enc, nil
}
