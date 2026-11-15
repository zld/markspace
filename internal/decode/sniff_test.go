package decode

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

func TestSniffBOMs(t *testing.T) {
	plain := []byte("hello 字幕")
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"utf8-bom", append(append([]byte{}, bomUTF8...), plain...), UTF8},
		{"utf16le-bom", mustEncode(t, utf16LEWithBOM(), string(plain)), UTF16LE},
		{"utf16be-bom", mustEncode(t, utf16BEWithBOM(), string(plain)), UTF16BE},
	}
	for _, c := range cases {
		text, label, err := Sniff(c.raw, "")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if label != c.want || text != string(plain) {
			t.Errorf("%s: label=%q text=%q", c.name, label, text)
		}
	}
}

func TestSniffValidUTF8NoBOM(t *testing.T) {
	raw := []byte("plain utf-8 without bom")
	label, got, err := func() (string, string, error) {
		s, l, e := Sniff(raw, "")
		return l, s, e
	}()
	if err != nil {
		t.Fatal(err)
	}
	if label != UTF8 || got != string(raw) {
		t.Errorf("got label=%q", label)
	}
}

func TestSniffGB18030RoundTrip(t *testing.T) {
	utf8Text := "简体中文测试"
	gb, _, err := transform.String(simplifiedchinese.GB18030.NewEncoder(), utf8Text)
	if err != nil {
		t.Fatal(err)
	}
	text, label, err := Sniff([]byte(gb), "gb18030")
	if err != nil {
		t.Fatalf("gb18030 decode: %v", err)
	}
	if label != GB18030 || text != utf8Text {
		t.Errorf("label=%q text=%q", label, text)
	}
}

func TestSniffBig5RoundTrip(t *testing.T) {
	utf8Text := "繁體中文測試"
	b5, _, err := transform.String(traditionalchinese.Big5.NewEncoder(), utf8Text)
	if err != nil {
		t.Fatal(err)
	}
	text, label, err := Sniff([]byte(b5), "Big5")
	if err != nil {
		t.Fatalf("big5 decode: %v", err)
	}
	if label != Big5 || text != utf8Text {
		t.Errorf("label=%q text=%q", label, text)
	}
}

func TestSniffRejectsInvalidBytes(t *testing.T) {
	// 0xFF 0xFE are not legal GB18030; strict re-encoding must fail rather
	// than emit replacement characters.
	bad := []byte{0xC4, 0xE3, 0xBA, 0xC3, 0xFF, 0xFE}
	if _, _, err := Sniff(bad, "gb18030"); err == nil {
		t.Fatal("invalid GB18030 bytes were silently accepted")
	}
}

func TestSniffAmbiguousRequiresEncoding(t *testing.T) {
	// A lone invalid UTF-8 byte with no BOM cannot be classified confidently.
	raw := []byte{0x80, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86}
	_, _, err := Sniff(raw, "")
	if err == nil || !strings.Contains(err.Error(), "--encoding") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
}

func TestCanonicalLabel(t *testing.T) {
	cases := map[string]string{
		"gbk":      GB18030,
		"UTF-16LE": UTF16LE,
		"big5":     Big5,
		"utf8":     UTF8,
	}
	for in, want := range cases {
		if got := canonicalLabel(in); got != want {
			t.Errorf("canonicalLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
