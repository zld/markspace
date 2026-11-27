package ebml

import "testing"

func TestNameOf(t *testing.T) {
	if got := NameOf(IDSegment); got != "Segment" {
		t.Errorf("NameOf(Segment) = %q", got)
	}
	if got := NameOf(IDCluster); got != "Cluster" {
		t.Errorf("NameOf(Cluster) = %q", got)
	}
	// Unregistered but canonical ID gets a hex fallback.
	fallback := NameOf(ElementID(0x81))
	if fallback != "0x81" {
		t.Errorf("fallback name = %q, want 0x81", fallback)
	}
	if !IsKnown(IDBlock) || IsKnown(ElementID(0x81)) {
		t.Error("IsKnown registry lookup wrong")
	}
}

func TestValidEBMLID(t *testing.T) {
	valid := []ElementID{IDEBMLHeader, IDSegment, IDTrackEntry, IDBlock, IDSimpleBlock, ElementID(0x81)}
	for _, id := range valid {
		if !ValidEBMLID(id) {
			t.Errorf("ID %x should be valid", uint64(id))
		}
	}
	// All-ones widths are reserved unknown markers, not element IDs.
	invalid := []ElementID{127, 16383, 0x0FFFFFFF}
	for _, id := range invalid {
		if ValidEBMLID(id) {
			t.Errorf("reserved all-ones ID %x must be invalid", uint64(id))
		}
	}
	// Values requiring more than four bytes cannot be Matroska IDs.
	if ValidEBMLID(ElementID(0x10000000)) {
		t.Error("5-byte ID must be invalid")
	}
}

func TestContainerAndPath(t *testing.T) {
	if !(&Element{ID: IDCluster}).IsContainer() {
		t.Error("Cluster must be a container")
	}
	if (&Element{ID: IDBlock}).IsContainer() {
		t.Error("Block must not be a container")
	}
	got := PathString([]ElementID{IDSegment, IDCluster, IDSimpleBlock})
	if got != "Segment/Cluster/SimpleBlock" {
		t.Errorf("PathString = %q", got)
	}
}

func TestElementOffsets(t *testing.T) {
	// ID VINT width 4 (0x1A45DFA3) followed by size VINT width 1.
	e := Element{ID: IDEBMLHeader, Offset: 0, HeaderSize: 5, Size: 10, PayloadOffset: 5}
	if e.Name() != "EBML" || e.PayloadOffset+e.Size != 15 {
		t.Errorf("element geometry wrong: %+v", e)
	}
}
