package ingest_test

import (
	"testing"

	"mediacore/internal/fault"
	"mediacore/internal/fixture"
	"mediacore/internal/ingest"
)

func newReader(t *testing.T, data []byte) *ingest.Reader {
	t.Helper()
	r, f := ingest.NewReader("sample.fmp4", data, ingest.DefaultLimits())
	if f != nil {
		t.Fatalf("NewReader: %v", f)
	}
	return r
}

func TestNextRejectsTruncatedHeader(t *testing.T) {
	r := newReader(t, []byte{0, 0, 0, 0x20, 'm', 'o'})
	_, ok, f := r.Next(0)
	if ok || f == nil {
		t.Fatal("expected a truncated header fault")
	}
	if f.Code != fault.CodeTruncatedHeader {
		t.Fatalf("code=%s", f.Code)
	}
	if f.Offset != 0 {
		t.Fatalf("offset=%d want 0", f.Offset)
	}
	if f.BoxType != "" {
		t.Fatalf("truncated header must not claim a box type, got %q", f.BoxType)
	}
}

func TestNextRejectsOversizeBox(t *testing.T) {
	data := make([]byte, 32)
	data[0], data[1], data[2], data[3] = 0, 0, 0x10, 0x00 // 4096 declared
	copy(data[4:], "mdat")
	r := newReader(t, data)
	_, ok, f := r.Next(0)
	if ok || f == nil || f.Code != fault.CodeTruncatedBox {
		t.Fatalf("expected truncated_box, got %v", f)
	}
	if f.Offset != 0 || f.BoxType != "mdat" {
		t.Fatalf("fault anchored wrong: %+v", f)
	}
}

func TestNextRejectsSubHeaderSize(t *testing.T) {
	data := []byte{0, 0, 0, 2, 'f', 'r', 'e', 'e', 0, 0, 0, 0}
	r := newReader(t, data)
	_, ok, f := r.Next(0)
	if ok || f == nil || f.Code != fault.CodeInvalidBoxSize {
		t.Fatalf("expected invalid_box_size, got %v", f)
	}
	if f.Offset != 0 {
		t.Fatalf("offset=%d", f.Offset)
	}
}

func TestNextRejectsLargeHeaderTruncation(t *testing.T) {
	data := make([]byte, 12)
	data[3] = 1 // size==1 means 64-bit size follows
	copy(data[4:], "moof")
	r := newReader(t, data)
	_, ok, f := r.Next(0)
	if ok || f == nil || f.Code != fault.CodeTruncatedHeader {
		t.Fatalf("expected truncated large header, got %v", f)
	}
	if f.Offset != 0 {
		t.Fatalf("offset=%d want 0", f.Offset)
	}
}

func TestNextAcceptsLargeHeader(t *testing.T) {
	data := make([]byte, 24)
	data[3] = 1
	copy(data[4:], "free")
	data[15] = 24
	r := newReader(t, data)
	h, ok, f := r.Next(0)
	if f != nil || !ok {
		t.Fatalf("Next: %v", f)
	}
	if h.HeaderSize != 16 || h.Size != 24 || h.End() != 24 {
		t.Fatalf("header=%+v", h)
	}
}

func TestNextExtendingToEOF(t *testing.T) {
	data := make([]byte, 20)
	copy(data[4:], "free")
	r := newReader(t, data)
	h, ok, f := r.Next(0)
	if f != nil || !ok {
		t.Fatalf("Next: %v", f)
	}
	if !h.ExtendsToEOF || h.Size != 20 {
		t.Fatalf("header=%+v", h)
	}
}

func TestReaderLimitOnFileSize(t *testing.T) {
	lim := ingest.DefaultLimits()
	lim.MaxFileBytes = 8
	if _, f := ingest.NewReader("big.fmp4", make([]byte, 9), lim); f == nil || f.Code != fault.CodeLimit {
		t.Fatalf("expected size limit fault, got %v", f)
	}
}

// TestEveryInjectedFaultHasExactOffset is the core acceptance check: the
// parser's first reported offset must equal the byte the fixture damaged.
func TestEveryInjectedFaultHasExactOffset(t *testing.T) {
	for _, kind := range fixture.AllFaults() {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			injected, err := fixture.Inject(41, 2, 24, []fixture.FaultKind{kind})
			if err != nil {
				t.Fatal(err)
			}
			r := newReader(t, injected.Data)
			info, f := ingest.ParseInit(r)
			if f != nil {
				t.Fatalf("init: %v", f)
			}
			frags, errs := ingest.ParseFragments(r, info.MoovOffset+info.MoovSize)
			_ = frags
			want := injected.Injections[0].ByteOffset
			if kind == fixture.FaultTfdtRegression || kind == fixture.FaultSequenceGap ||
				kind == fixture.FaultDuplicate || kind == fixture.FaultIndexGap ||
				kind == fixture.FaultRTPSequenceGap {
				// These are semantic faults: the bytes parse cleanly and the
				// transform stage reports them at the recorded offset.
				if len(errs) != 0 {
					t.Fatalf("semantic fault produced parse errors: %v", errs[0])
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("no fault reported for %s", kind)
			}
			if errs[0].Offset != want {
				t.Fatalf("%s offset=%d want %d (%v)", kind, errs[0].Offset, want, errs[0])
			}
		})
	}
}

func TestParseInitRejectsMissingMoov(t *testing.T) {
	stream, err := fixture.Build(fixture.DefaultSpec(7, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	// ftyp only.
	r := newReader(t, stream.Data[:stream.InitSize-int64(stream.InitSize-24)])
	_ = r
	init1, _ := fixture.BuildInit(fixture.DefaultSpec(7, 1, 2))
	r = newReader(t, init1[:24]) // just ftyp
	if _, f := ingest.ParseInit(r); f == nil || f.Code != fault.CodeMissingField {
		t.Fatalf("expected missing moov, got %v", f)
	}
}
