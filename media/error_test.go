package media

import (
	"encoding/json"
	"testing"
)

func TestErrorSerializes(t *testing.T) {
	e := New(CatChecksum, StageScan, "digest mismatch").
		WithOffset(4350).WithSeq(7)
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["category"] != string(CatChecksum) || back["stage"] != string(StageScan) {
		t.Fatalf("serialized=%s", b)
	}
	if back["frame_offset"].(float64) != 4350 {
		t.Fatalf("offset lost: %s", b)
	}
	if back["suggestion"] == "" {
		t.Fatal("missing recovery suggestion")
	}
}

func TestRecoverableCategories(t *testing.T) {
	recoverable := []Category{CatTruncatedFrame, CatChecksum, CatPacketGap,
		CatIndexInput, CatIndexVersion, CatIndexTruncated, CatManifestMismatch}
	for _, c := range recoverable {
		if !New(c, StageScan, "x").Recoverable {
			t.Fatalf("%s should be recoverable", c)
		}
	}
	if New(CatBadMagic, StageScan, "x").Recoverable {
		t.Fatal("bad magic should not be marked recoverable")
	}
}

func TestWrapPreservesCause(t *testing.T) {
	inner := New(CatChecksum, StageScan, "inner")
	wrapped := Wrap(CatIndexInput, StageIndex, inner, "outer")
	if wrapped.Unwrap() != inner {
		t.Fatal("wrap lost the cause")
	}
	if CategoryOf(wrapped) != CatIndexInput {
		t.Fatalf("category=%s", CategoryOf(wrapped))
	}
}
