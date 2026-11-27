package fault

import (
	"errors"
	"strings"
	"testing"
)

func TestFaultFormatting(t *testing.T) {
	f := New(StageParse, KindTruncated, "short packet %d", 7).AtOffset(188 * 3).Recover(false)
	got := f.Error()
	for _, want := range []string{"parse/truncated-ts", "offset=564", "recoverable=false", "short packet 7"} {
		if !strings.Contains(got, want) {
			t.Fatalf("fault string %q missing %q", got, want)
		}
	}
}

func TestFaultDefaults(t *testing.T) {
	f := New(StageCLI, KindInvalidArg, "bad")
	if f.Offset != -1 || f.Sample != -1 {
		t.Fatalf("position sentinel not set: %+v", f)
	}
	if f.Recoverable {
		t.Fatal("fault must default to non-recoverable")
	}
}

func TestListCountsAndRecoverability(t *testing.T) {
	l := &List{}
	l.Add(New(StageParse, KindCCJump, "jump").AtOffset(10).Recover(true))
	l.Add(New(StageParse, KindCCJump, "jump2").AtOffset(20).Recover(true))
	l.Add(New(StageParse, KindTruncated, "cut").AtOffset(30))
	if l.Recoverable() {
		t.Fatal("list with a hard fault must not be recoverable")
	}
	counts := l.CountByKind()
	if len(counts) != 2 || counts[0].Kind != KindCCJump || counts[0].Count != 2 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
	if l.FirstNonRecoverable() == nil || l.FirstNonRecoverable().Kind != KindTruncated {
		t.Fatal("wrong first hard fault")
	}
}

func TestEmptyList(t *testing.T) {
	var l *List
	if l.Len() != 0 || !l.Recoverable() || l.CountByKind() != nil {
		t.Fatal("nil list semantics broken")
	}
	if l.FirstNonRecoverable() != nil {
		t.Fatal("nil list must have no hard fault")
	}
	if Merge(nil, &List{}).Len() != 0 {
		t.Fatal("merge broken")
	}
}

func TestAsList(t *testing.T) {
	l := &List{}
	wrapped := errors.Join(l)
	got, ok := AsList(wrapped)
	if !ok || got != l {
		t.Fatal("AsList failed through wrapped chain")
	}
}
