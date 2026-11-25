package segment

import "testing"

func TestSampleCacheReleasesSegmentPayloads(t *testing.T) {
	c := NewSampleCache()
	payloads := [][]byte{make([]byte, 16), make([]byte, 24)}
	if err := ProcessBatch(c, "seg-000", payloads, func(got [][]byte) error {
		if len(got) != 2 || c.RetainedBytes() != 40 {
			t.Fatalf("batch not retained: count=%d bytes=%d", len(got), c.RetainedBytes())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c.RetainedBytes() != 0 || c.Get("seg-000") != nil {
		t.Fatalf("cache not released: bytes=%d batch=%v", c.RetainedBytes(), c.Get("seg-000"))
	}
}
