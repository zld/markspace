// Package charset detects and decodes the text encodings used by subtitle
// files. UTF-8 (with or without BOM) and UTF-16 BOM variants are recognised
// deterministically; legacy GBK and Shift-JIS content is scored heuristically
// and ambiguous data must be resolved with an explicit --encoding flag.
package charset

import (
	"bytes"
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Encoding names reported to the user and accepted on the command line.
const (
	UTF8     = "utf-8"
	UTF16LE  = "utf-16le"
	UTF16BE  = "utf-16be"
	GBK      = "gbk"
	ShiftJIS = "shift-jis"
)

var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// Result describes a successful encoding detection.
type Result struct {
	Name string
	BOM  bool
	// Skip is the number of leading BOM bytes to remove after decoding.
	Skip int
}

// ErrUnknown is returned when no encoding can be decided. The caller should
// ask the user to pass --encoding explicitly.
var ErrUnknown = errors.New("encoding cannot be determined; specify it explicitly with --encoding")

// Sniff inspects raw file bytes and decides which encoding they use.
func Sniff(b []byte) (Result, error) {
	switch {
	case bytes.HasPrefix(b, bomUTF8):
		return Result{Name: UTF8, BOM: true, Skip: len(bomUTF8)}, nil
	case bytes.HasPrefix(b, bomUTF16LE):
		return Result{Name: UTF16LE, BOM: true, Skip: 2}, nil
	case bytes.HasPrefix(b, bomUTF16BE):
		return Result{Name: UTF16BE, BOM: true, Skip: 2}, nil
	}
	if utf8.Valid(b) {
		return Result{Name: UTF8, BOM: false}, nil
	}
	name, ok := guessLegacy(b)
	if !ok {
		return Result{}, ErrUnknown
	}
	return Result{Name: name, BOM: false}, nil
}

// guessLegacy scores GBK against Shift-JIS. Both encodings consume lead/trail
// byte pairs, so the whole buffer must decode cleanly (no replacement runes)
// under the winner, and characteristic byte ranges then break ties.
func guessLegacy(b []byte) (string, bool) {
	gbkOK, gbkScore := decodes(simplifiedchinese.GBK, b, scoreGBK, nil)
	sjOK, sjScore := decodes(japanese.ShiftJIS, b, scoreSJIS, func(c byte) bool {
		return c >= 0xA1 && c <= 0xDF // half-width katakana
	})
	switch {
	case gbkOK && !sjOK:
		return GBK, true
	case sjOK && !gbkOK:
		return ShiftJIS, true
	case !gbkOK && !sjOK:
		return "", false
	}
	// Both decode cleanly: the encoding whose grammar fits the byte stream
	// better wins. A clear margin is required or the data is genuinely
	// ambiguous and must be resolved with --encoding.
	if gbkScore > sjScore+1 {
		return GBK, true
	}
	if sjScore > gbkScore+1 {
		return ShiftJIS, true
	}
	return "", false
}

// pairScore inspects a lead/trail pair and returns how strongly it speaks for
// the candidate encoding: positive evidence, negative evidence, or zero when
// the pair is legal in both grammars.
type pairScore func(lead, trail byte) int

// singleByteLead reports lead bytes that stand alone (Shift-JIS half-width
// kana); nil means every high byte starts a two-byte pair (GBK).
func decodes(enc encoding.Encoding, b []byte, score pairScore, single func(byte) bool) (ok bool, scoreSum int) {
	out, _, err := transform.Bytes(enc.NewDecoder(), b)
	if err != nil || bytes.ContainsRune(out, utf8.RuneError) || !utf8.Valid(out) {
		return false, 0
	}
	for i := 0; i < len(b); i++ {
		lead := b[i]
		if lead < 0x80 { // ASCII byte
			continue
		}
		if single != nil && single(lead) {
			continue // one-byte character; the next byte starts fresh
		}
		if i+1 >= len(b) {
			return false, 0 // dangling lead byte
		}
		scoreSum += score(lead, b[i+1])
		i++ // consumed the trail byte
	}
	return true, scoreSum
}

// scoreGBK rewards GBK-only trail bytes (0x40-0x7E) and the GBK-heavy high
// lead range (0xB0-0xF7), while penalising Shift-JIS-only grammar.
func scoreGBK(lead, trail byte) int {
	s := 0
	if trail >= 0x40 && trail <= 0x7E {
		s += 2 // illegal as a Shift-JIS trail byte
	}
	if lead >= 0xB0 && lead <= 0xF7 {
		s++ // common GBK level-1 hanzi range
	}
	if lead >= 0x81 && lead <= 0x9F || lead >= 0xE0 && lead <= 0xEF {
		s-- // Shift-JIS JIS X 0208 lead range
	}
	return s
}

// scoreSJIS rewards Shift-JIS lead ranges and half-width kana single bytes.
func scoreSJIS(lead, trail byte) int {
	s := 0
	if lead >= 0x81 && lead <= 0x9F || lead >= 0xE0 && lead <= 0xEF {
		s += 2
	}
	if trail >= 0x40 && trail <= 0x7E {
		s -= 2 // pair would be illegal here under Shift-JIS
	}
	if lead >= 0xB0 && lead <= 0xF7 && !(lead >= 0xE0 && lead <= 0xEF) {
		s--
	}
	return s
}

// namedEncoding maps a command line encoding name to an x/text encoding.
func namedEncoding(name string) (encoding.Encoding, error) {
	switch name {
	case GBK:
		return simplifiedchinese.GBK, nil
	case ShiftJIS, "sjis":
		return japanese.ShiftJIS, nil
	default:
		return nil, fmt.Errorf("unsupported explicit encoding %q", name)
	}
}

// Decode converts raw bytes into UTF-8 text. When explicit is empty the
// encoding is sniffed; otherwise the named legacy encoding is forced.
func Decode(b []byte, explicit string) (text []byte, detected Result, err error) {
	if explicit != "" {
		switch explicit {
		case UTF8:
			detected = Result{Name: UTF8, BOM: bytes.HasPrefix(b, bomUTF8)}
		case UTF16LE, UTF16BE:
			return nil, Result{}, fmt.Errorf("explicit %s is handled via BOM only", explicit)
		default:
			enc, encErr := namedEncoding(explicit)
			if encErr != nil {
				return nil, Result{}, encErr
			}
			text, _, err = transform.Bytes(enc.NewDecoder(), b)
			if err != nil {
				return nil, Result{}, fmt.Errorf("decode as %s: %w", explicit, err)
			}
			return text, Result{Name: explicit}, nil
		}
	} else {
		detected, err = Sniff(b)
		if err != nil {
			return nil, Result{}, err
		}
	}
	switch detected.Name {
	case UTF8:
		raw := b
		if detected.BOM {
			raw = b[detected.Skip:]
		}
		if !utf8.Valid(raw) {
			return nil, Result{}, errors.New("declared utf-8 data contains invalid bytes")
		}
		return raw, detected, nil
	case UTF16LE, UTF16BE:
		enc := unicodeEncoding(detected.Name)
		text, _, err = transform.Bytes(enc.NewDecoder(), b[detected.Skip:])
		if err != nil {
			return nil, Result{}, fmt.Errorf("decode %s: %w", detected.Name, err)
		}
		return text, detected, nil
	default:
		enc, encErr := namedEncoding(detected.Name)
		if encErr != nil {
			return nil, Result{}, encErr
		}
		text, _, err = transform.Bytes(enc.NewDecoder(), b)
		if err != nil {
			return nil, Result{}, fmt.Errorf("decode %s: %w", detected.Name, err)
		}
		return text, detected, nil
	}
}

func unicodeEncoding(name string) encoding.Encoding {
	if name == UTF16BE {
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
	}
	return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
}
