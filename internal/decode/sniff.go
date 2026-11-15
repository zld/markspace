// Package decode detects the character encoding of raw subtitle bytes and
// converts them to UTF-8. Decoding is strict: an input that cannot be
// round-tripped through its declared encoding is rejected instead of being
// silently repaired with replacement characters.
package decode

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

// Canonical encoding labels reported in JSONL output.
const (
	UTF8    = "UTF-8"
	UTF16LE = "UTF-16LE"
	UTF16BE = "UTF-16BE"
	GB18030 = "GB18030"
	Big5    = "Big5"
)

// BOM prefixes defined by the Unicode standard.
var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// lookup maps accepted user labels (lower-cased) to canonical names.
var lookup = map[string]string{
	"utf-8":    UTF8,
	"utf8":     UTF8,
	"utf-16":   UTF16LE,
	"utf16":    UTF16LE,
	"utf-16le": UTF16LE,
	"utf16le":  UTF16LE,
	"utf-16be": UTF16BE,
	"utf16be":  UTF16BE,
	"gb18030":  GB18030,
	"gbk":      GB18030,
	"gb2312":   GB18030,
	"big5":     Big5,
}

// charsetNames maps canonical labels to IANA names understood by x/net.
var charsetNames = map[string]string{
	UTF16LE: "utf-16le",
	UTF16BE: "utf-16be",
	GB18030: "gb18030",
	Big5:    "big5",
}

// Sniff decodes raw bytes to UTF-8 and returns the canonical encoding label.
//
// A BOM always wins. Otherwise valid UTF-8 is accepted as UTF-8; declared is
// honored when supplied; without either, the content is classified with the
// x/net charset detector, which must return a confident, supported answer.
// Ambiguous content produces an error asking the caller to pass --encoding.
func Sniff(raw []byte, declared string) (text, label string, err error) {
	declared = canonicalLabel(declared)

	switch {
	case bytes.HasPrefix(raw, bomUTF8):
		return decodeStrict(bytes.TrimPrefix(raw, bomUTF8), UTF8)
	case bytes.HasPrefix(raw, bomUTF16LE):
		return decodeStrict(bytes.TrimPrefix(raw, bomUTF16LE), UTF16LE)
	case bytes.HasPrefix(raw, bomUTF16BE):
		return decodeStrict(bytes.TrimPrefix(raw, bomUTF16BE), UTF16BE)
	}

	if declared != "" {
		return decodeStrict(raw, declared)
	}
	if utf8.Valid(raw) {
		return decodeStrict(raw, UTF8)
	}

	guess, certain := detect(raw)
	if !certain || !supported(guess) {
		return "", "", fmt.Errorf(
			"encoding cannot be determined confidently (best guess %q); specify --encoding explicitly", guess)
	}
	return decodeStrict(raw, guess)
}

// decodeStrict runs the encoding and then verifies losslessness: the decoded
// text must be valid UTF-8 and re-encoding must reproduce the original bytes
// byte for byte.
func decodeStrict(raw []byte, label string) (string, string, error) {
	enc, err := encodingFor(label)
	if err != nil {
		return "", "", err
	}
	decoded, _, err := transform.String(enc.NewDecoder(), string(raw))
	if err != nil {
		return "", "", fmt.Errorf("decode as %s failed: %w", label, err)
	}
	if !utf8.ValidString(decoded) {
		return "", "", fmt.Errorf("decode as %s produced invalid UTF-8", label)
	}
	reEncoded, _, err := transform.String(enc.NewEncoder(), decoded)
	if err != nil {
		return "", "", fmt.Errorf("re-encode as %s failed: %w", label, err)
	}
	if reEncoded != string(raw) {
		return "", "", fmt.Errorf("input is not valid %s (invalid byte sequence); refusing to replace bytes", label)
	}
	return decoded, label, nil
}

func encodingFor(label string) (encoding.Encoding, error) {
	if label == UTF8 {
		return encoding.Nop, nil
	}
	name, ok := charsetNames[label]
	if !ok {
		return nil, fmt.Errorf("unsupported encoding %q", label)
	}
	enc, _ := charset.Lookup(name)
	if enc == nil {
		return nil, fmt.Errorf("unsupported encoding %q", label)
	}
	return enc, nil
}

// detect classifies BOM-less bytes using the x/net heuristic, returning the
// canonical label and whether the detector considers the answer certain.
func detect(raw []byte) (string, bool) {
	_, name, certain := charset.DetermineEncoding(raw, "")
	if name == "" {
		return "", false
	}
	return canonicalLabel(name), certain
}

func canonicalLabel(s string) string {
	if s == "" {
		return ""
	}
	if l, ok := lookup[strings.ToLower(strings.TrimSpace(s))]; ok {
		return l
	}
	return strings.ToUpper(strings.TrimSpace(s))
}

func supported(label string) bool {
	switch label {
	case UTF8, UTF16LE, UTF16BE, GB18030, Big5:
		return true
	}
	return false
}
