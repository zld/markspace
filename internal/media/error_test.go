package media

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormattingCarriesLocation(t *testing.T) {
	e := &Error{
		Code:    CodeTruncatedBox,
		Op:      "parse.mp4",
		File:    "sample.mp4",
		Offset:  16,
		TrackID: 2,
		HasTime: true,
		From:    FromMillis(100),
		To:      FromMillis(200),
		Msg:     "mdat runs past EOF",
	}
	got := e.Error()
	for _, want := range []string{
		"TRUNCATED_BOX", "offset=16", "track=2",
		"range=[1/10s,1/5s)", "mdat runs past EOF",
	} {
		if !contains(got, want) {
			t.Fatalf("error %q missing %q", got, want)
		}
	}
}

func TestCodeOfAndWrap(t *testing.T) {
	inner := New(CodeMXFOutOfRange, "mxf.klv", 0, "length exceeds file")
	wrapped := Wrap(inner, CodeIO, "ignored", 5)
	me, ok := AsError(wrapped)
	if !ok || me.Code != CodeMXFOutOfRange || me.Op != "mxf.klv" {
		t.Fatalf("wrap lost model: %v", wrapped)
	}

	plain := Wrap(errors.New("disk gone"), CodeIO, "read", -1)
	if CodeOf(plain) != CodeIO {
		t.Fatalf("code=%s", CodeOf(plain))
	}
	var me2 *Error
	if !errors.As(plain, &me2) || me2.Offset != -1 {
		t.Fatalf("offset must be -1 when unknown: %#v", me2)
	}
	if CodeOf(nil) != "" {
		t.Fatal("nil code")
	}
	if CodeOf(errors.New("x")) != CodeInvalidBox {
		t.Fatal("unknown errors must not look like IO")
	}
}

func TestErrorUnwrap(t *testing.T) {
	root := fmt.Errorf("root")
	e := &Error{Code: CodeIO, Err: root}
	if !errors.Is(e, root) {
		t.Fatal("unwrap")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
