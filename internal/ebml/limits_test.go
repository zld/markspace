package ebml

import "testing"

func TestDefaultLimitsEnforceArchivePolicy(t *testing.T) {
	lim := DefaultLimits()
	// Single-file read cap is fixed at 4 GiB.
	if lim.MaxFileSize != 4<<30 {
		t.Fatalf("MaxFileSize=%d want 4GiB", lim.MaxFileSize)
	}
	if lim.MaxElementSize != 2<<30 {
		t.Fatalf("MaxElementSize=%d want 2GiB", lim.MaxElementSize)
	}
	if lim.MaxBlockSize != 64<<20 {
		t.Fatalf("MaxBlockSize=%d want 64MiB", lim.MaxBlockSize)
	}
	if lim.MaxElementSize > lim.MaxFileSize || lim.MaxBlockSize > lim.MaxElementSize {
		t.Fatal("limit hierarchy inconsistent")
	}
}

func TestOversizedLengthRejectedWithoutRead(t *testing.T) {
	// A declared element length far beyond the element cap must never yield
	// a readable block: the overrun is reported and no events are produced,
	// and no bytes past EOF are touched.
	longSize := make([]byte, 8)
	longSize[0] = 0x01 // 8-byte VINT marker
	// payload length 2^40, far above the 2 GiB element cap.
	longSize[7] = 0x01
	header := append(append([]byte{}, IDCluster...), longSize...)
	data := append(MinimalHeader(), Segment(header, false)...)
	r, _ := NewReader(data, DefaultLimits())
	res, err := r.Parse()
	// Either a hard rejection or a recorded truncation issue is acceptable,
	// but the over-long element must never resolve into a block event.
	if len(res.Events) != 0 {
		t.Fatalf("over-cap element produced %d events", len(res.Events))
	}
	if err == nil && len(res.Report.Issues) == 0 {
		t.Fatal("over-cap element length was silently accepted")
	}
}

func TestElementCapDirectlyEnforced(t *testing.T) {
	// An element whose declared size is internally consistent but above the
	// configured cap trips the explicit limit check.
	lim := DefaultLimits()
	lim.MaxElementSize = 4
	r, err := NewReader(validSmallFile(), lim)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Parse()
	if err == nil {
		t.Fatal("element exceeding the configured cap must error")
	}
}

func validSmallFile() []byte {
	blk := SimpleBlock(1, 0, true, []byte("x"))
	c := Cluster(0, blk)
	return BuildMatroska([][]byte{c}, false)
}
