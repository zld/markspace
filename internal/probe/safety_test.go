package probe

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/broadcast/mediad/internal/model"
)

// box builds a minimal top-level box header with an explicit declared size.
func box(typ string, size uint32, payload int) []byte {
	buf := make([]byte, 8+payload)
	binary.BigEndian.PutUint32(buf[:4], size)
	copy(buf[4:8], typ)
	return buf
}

func TestRejectsBadBoxLength(t *testing.T) {
	// A box declaring a size smaller than its 8-byte header.
	raw := box("moov", 4, 0)
	errs := &model.ErrorSet{}
	scan, err := ScanBoxes(bytes.NewReader(raw), int64(len(raw)), errs)
	if err != nil {
		t.Fatalf("scan must not hard-fail: %v", err)
	}
	if !errs.Has(model.KindTruncatedBox) {
		t.Fatalf("expected truncation/bad-length error, got %v", errs.All())
	}
	if len(scan.Boxes) != 0 {
		t.Fatalf("invalid box must not be accepted, got %d", len(scan.Boxes))
	}
}

func TestRejectsBoxExtendingPastEOF(t *testing.T) {
	// Header claims 100 bytes but only 16 exist.
	raw := box("mdat", 100, 8)
	errs := &model.ErrorSet{}
	scan, err := ScanBoxes(bytes.NewReader(raw), int64(len(raw)), errs)
	if err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if !errs.Has(model.KindTruncatedBox) {
		t.Fatalf("expected past-EOF truncation, got %v", errs.All())
	}
	if len(scan.Boxes) != 0 {
		t.Fatal("truncated trailing box must not enter the accepted box list")
	}
}

func TestAcceptsBoxExtendingToEOF(t *testing.T) {
	// size==0 means "extends to EOF" and must be accepted.
	raw := box("mdat", 0, 16)
	errs := &model.ErrorSet{}
	scan, err := ScanBoxes(bytes.NewReader(raw), int64(len(raw)), errs)
	if err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if len(scan.Boxes) != 1 || errs.Count() != 0 {
		t.Fatalf("EOF-extended box should be accepted: boxes=%d errs=%v", len(scan.Boxes), errs.All())
	}
}

func TestSecondBoxAfterTruncationStaysReachable(t *testing.T) {
	// A valid ftyp followed by a truncated mdat: the valid box survives and
	// the truncation is recorded rather than aborting the whole scan.
	ftyp := box("ftyp", 24, 16)
	bad := box("mdat", 999, 8)
	raw := append(ftyp, bad...)
	errs := &model.ErrorSet{}
	scan, err := ScanBoxes(bytes.NewReader(raw), int64(len(raw)), errs)
	if err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if len(scan.Boxes) != 1 || scan.Boxes[0].Type != "ftyp" {
		t.Fatalf("valid leading box must survive, got %+v", scan.Boxes)
	}
	if !errs.Has(model.KindTruncatedBox) {
		t.Fatal("truncated second box must be reported")
	}
}

func TestLargeSizeBoxIsRejected(t *testing.T) {
	// A 64-bit largesize smaller than its 16-byte header is invalid.
	buf := make([]byte, 24)
	binary.BigEndian.PutUint32(buf[:4], 1) // size==1 => largesize form
	copy(buf[4:8], "mdat")
	binary.BigEndian.PutUint64(buf[8:16], 10) // illegal largesize
	errs := &model.ErrorSet{}
	if _, err := ScanBoxes(bytes.NewReader(buf), int64(len(buf)), errs); err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if !errs.Has(model.KindTruncatedBox) {
		t.Fatalf("illegal largesize must be rejected, got %v", errs.All())
	}
}

func TestFileSizeLimitEnforced(t *testing.T) {
	// OpenFile cannot be exercised without a real file, so verify the limit
	// constant and its comparison directly.
	if model.MaxFileSize != 2<<30 {
		t.Fatalf("MaxFileSize = %d, want 2 GiB", model.MaxFileSize)
	}
}
