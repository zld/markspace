package errors

import (
	"errors"
	"testing"
)

func TestMediaErrorModel(t *testing.T) {
	me := At(CodeTruncated, 4096, "box %s extends past EOF", "mdat")
	if me.Offset != 4096 || me.Code != CodeTruncated {
		t.Fatalf("unexpected error: %+v", me)
	}
	if got := me.Error(); got != "truncated at offset 4096: box mdat extends past EOF" {
		t.Fatalf("unexpected message: %q", got)
	}
	if CodeTruncated.ExitCode() != 2 || CodeIndexMismatch.ExitCode() != 3 || CodeTimeWentBack.ExitCode() != 1 {
		t.Fatal("exit code mapping broken")
	}
	wrapped := fmtWrap(me)
	var target *MediaError
	if !As(wrapped, &target) || target.Code != CodeTruncated {
		t.Fatal("As did not find wrapped MediaError")
	}
	if AsCode(nil) != CodeOK || AsCode(errors.New("plain")) == CodeOK {
		t.Fatal("AsCode classification broken")
	}
}

func fmtWrap(err error) error { return &wrap{err} }

type wrap struct{ err error }

func (w *wrap) Error() string { return w.err.Error() }
func (w *wrap) Unwrap() error { return w.err }
