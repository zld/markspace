package charset

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

func TestSniffUTF8AndBOMs(t *testing.T) {
	plain := "1\n00:00:01,000 --> 00:00:02,000\nhello\n"
	if _, enc, err := Decode([]byte(plain), ""); err != nil || enc != UTF8 {
		t.Fatalf("plain utf-8: enc=%s err=%v", enc, err)
	}
	if s, enc, err := Decode(append([]byte{0xEF, 0xBB, 0xBF}, plain...), ""); err != nil || enc != UTF8BOM || s != plain {
		t.Fatalf("utf-8 bom: enc=%s err=%v", enc, err)
	}
	u16 := func(s string, e unicode.Endianness, bom bool) []byte {
		b, err := unicode.UTF16(e, unicode.UseBOM).NewEncoder().Bytes([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		if !bom { // strip BOM the encoder added
			b = b[2:]
		}
		return b
	}
	if _, enc, err := Decode(u16(plain, unicode.LittleEndian, true), ""); err != nil || enc != UTF16LEBOM {
		t.Fatalf("utf-16le bom: enc=%s err=%v", enc, err)
	}
	if _, enc, err := Decode(u16(plain, unicode.BigEndian, true), ""); err != nil || enc != UTF16BEBOM {
		t.Fatalf("utf-16be bom: enc=%s err=%v", enc, err)
	}
	if s, enc, err := Decode(u16(plain, unicode.LittleEndian, false), ""); err != nil || enc != UTF16LE || s != plain {
		t.Fatalf("utf-16le no bom: enc=%s err=%v s=%q", enc, err, s)
	}
	if s, enc, err := Decode(u16(plain, unicode.BigEndian, false), ""); err != nil || enc != UTF16BE || s != plain {
		t.Fatalf("utf-16be no bom: enc=%s err=%v s=%q", enc, err, s)
	}
}

func TestSniffUndetectable(t *testing.T) {
	bad := []byte{0x81, 0x40, 0x82, 0x41, 0xFE, 0x42} // not utf-8, no utf-16 pattern
	if _, _, err := Decode(bad, ""); err == nil {
		t.Fatal("expected undetectable-encoding error")
	}
	if _, _, err := Decode([]byte{0xFF, 0xFE, 0x61}, ""); err == nil {
		t.Fatal("expected odd-length utf-16 error")
	}
	if _, _, err := Decode([]byte{0xEF, 0xBB, 0xBF, 0xFF, 0xFF}, ""); err == nil {
		t.Fatal("expected invalid utf-8 body error")
	}
}

func TestExplicitEncodings(t *testing.T) {
	gb, err := simplifiedchinese.GB18030.NewEncoder().String("守卫：站住！")
	if err != nil {
		t.Fatal(err)
	}
	s, enc, err := Decode([]byte(gb), "gb18030")
	if err != nil || enc != GB18030 || s != "守卫：站住！" {
		t.Fatalf("gb18030: enc=%s err=%v s=%q", enc, err, s)
	}
	if _, _, err := Decode([]byte{0xFF, 0xFE, 0xFD}, "gbk"); err == nil {
		t.Fatal("expected gbk decode error for invalid bytes")
	}
	s, _, err = Decode([]byte{0x93, 0x68, 0x69, 0x94}, "windows-1252")
	if err != nil || s != "“hi”" {
		t.Fatalf("windows-1252: err=%v s=%q", err, s)
	}
	if _, _, err := Decode([]byte("ok"), "latin9"); err == nil {
		t.Fatal("expected unsupported encoding error")
	}
	if !Supported("GBK") || Supported("latin9") {
		t.Fatal("Supported mismatch")
	}
	if !strings.Contains("utf-8", "utf") {
		t.Fatal("sanity")
	}
}
