package ebml

import "testing"

// TestUnknownChildInKnownParent checks that an unknown-length Cluster inside a
// known-length Segment ends at the next Cluster and the Segment still parses
// every sibling.
func TestUnknownChildInKnownParent(t *testing.T) {
	mk := func(frame byte) []byte {
		var c Builder
		_ = c.UintElement(IDTimestamp, 0)
		_ = c.Element(IDSimpleBlock, blockBody(frame))
		return c.Bytes()
	}
	var seg Builder
	_ = seg.UnknownElement(IDCluster, mk(0x11))
	_ = seg.UnknownElement(IDCluster, mk(0x22))
	// Segment has a known (explicit) length around the two unknown clusters.
	var top Builder
	_ = top.Element(IDSegment, seg.Bytes())
	doc, err := Parse(top.Bytes())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	clusters := doc.All(IDCluster)
	if len(clusters) != 2 {
		t.Fatalf("clusters=%d, want 2", len(clusters))
	}
	if clusters[0].PayloadEnd != clusters[1].Offset {
		t.Errorf("cluster 0 end=%d, want %d", clusters[0].PayloadEnd, clusters[1].Offset)
	}
	seg1 := doc.Find(IDSegment)
	if seg1.PayloadEnd != int64(len(doc.Data)) {
		t.Errorf("known-length segment end=%d, want %d", seg1.PayloadEnd, len(doc.Data))
	}
}
