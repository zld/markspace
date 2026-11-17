package charset

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func mustEncode(t *testing.T, enc encoding.Encoding, s string) []byte {
	t.Helper()
	b, _, err := transform.Bytes(enc.NewEncoder(), []byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSniffUTF8(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		bom  bool
	}{
		{"plain", []byte("1\n00:00:01,000 --> 00:00:02,000\nhello"), false},
		{"bom", append(append([]byte{}, bomUTF8...), []byte("hello")...), true},
	} {
		got, err := Sniff(tc.data)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.Name != UTF8 || got.BOM != tc.bom {
			t.Fatalf("%s: got %+v", tc.name, got)
		}
	}
}

func TestSniffUTF16BOM(t *testing.T) {
	le := append(append([]byte{}, bomUTF16LE...), mustEncode(t, unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), "hello")...)
	be := append(append([]byte{}, bomUTF16BE...), mustEncode(t, unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM), "hello")...)
	if r, err := Sniff(le); err != nil || r.Name != UTF16LE || !r.BOM {
		t.Fatalf("utf16le: %+v %v", r, err)
	}
	if r, err := Sniff(be); err != nil || r.Name != UTF16BE || !r.BOM {
		t.Fatalf("utf16be: %+v %v", r, err)
	}
	out, r, err := Decode(le, "")
	if err != nil || r.Name != UTF16LE || string(out) != "hello" {
		t.Fatalf("decode le: %q %+v %v", out, r, err)
	}
}

func TestSniffLegacy(t *testing.T) {
	gbkBytes := mustEncode(t, simplifiedchinese.GBK, "字幕测试\n第二行")
	sjisBytes := mustEncode(t, japanese.ShiftJIS, "字幕テスト\n二行目")
	if r, err := Sniff(gbkBytes); err != nil || r.Name != GBK {
		t.Fatalf("gbk: %+v %v", r, err)
	}
	if r, err := Sniff(sjisBytes); err != nil || r.Name != ShiftJIS {
		t.Fatalf("sjis: %+v %v", r, err)
	}
	out, r, err := Decode(gbkBytes, "")
	if err != nil || r.Name != GBK || string(out) != "字幕测试\n第二行" {
		t.Fatalf("decode gbk: %q %+v %v", out, r, err)
	}
}

func TestExplicitEncoding(t *testing.T) {
	gbkBytes := mustEncode(t, simplifiedchinese.GBK, "强制编码")
	out, r, err := Decode(gbkBytes, GBK)
	if err != nil || r.Name != GBK || !bytes.Contains(out, []byte("强制编码")) {
		t.Fatalf("explicit gbk: %q %+v %v", out, r, err)
	}
	if _, _, err := Decode(gbkBytes, "utf-8"); err == nil {
		t.Fatal("invalid utf-8 must be rejected when forced")
	}
}

func TestExplicitEncodingResolvesOverride(t *testing.T) {
	// Bytes that decode under both grammars; forcing either encoding must
	// return exactly what that decoder produces without a sniff error.
	amb := []byte{0x82, 0x60, 0x82, 0x60}
	out, r, err := Decode(amb, ShiftJIS)
	if err != nil || r.Name != ShiftJIS || len(out) == 0 {
		t.Fatalf("explicit shift-jis override failed: %q %+v %v", out, r, err)
	}
	out, r, err = Decode(amb, GBK)
	if err != nil || r.Name != GBK || len(out) == 0 {
		t.Fatalf("explicit gbk override failed: %q %+v %v", out, r, err)
	}
}

func TestUnknownEncoding(t *testing.T) {
	// High bytes that are illegal lead bytes in both GBK and Shift-JIS,
	// chosen so the prefix does not mimic a UTF-16 BOM.
	junk := []byte{0x80, 0xFE, 0x80, 0xFD, 0xFE, 0x80, 0xFD, 0xFE}
	if _, err := Sniff(junk); err == nil {
		t.Fatal("expected undecidable encoding error")
	}
	if _, _, err := Decode(junk, ""); err == nil {
		t.Fatal("Decode must surface the undecidable encoding error")
	}
}
