package sample

import "testing"

func TestGenerateIsDeterministicAndLocatesLossBoundary(t *testing.T) {
	a, err := Generate(Config{Seed: 17, Loss: []int{4, 9}, Reorder: 3})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(Config{Seed: 17, Loss: []int{4, 9}, Reorder: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Packets) != len(b.Packets) {
		t.Fatalf("packet counts differ: %d vs %d", len(a.Packets), len(b.Packets))
	}
	for i := range a.Packets {
		if string(a.Packets[i].Raw) != string(b.Packets[i].Raw) {
			t.Fatalf("packet %d is not deterministic", i)
		}
	}
	if a.FirstBadOffset != 4*SlotSize {
		t.Fatalf("first bad offset=%d want %d", a.FirstBadOffset, 4*SlotSize)
	}
	if len(a.All) != 25 || len(a.Packets) != 23 {
		t.Fatalf("all=%d observed=%d", len(a.All), len(a.Packets))
	}
	if encoded := a.EncodeFixedSlots(); len(encoded) != 25*int(SlotSize) {
		t.Fatalf("encoded length=%d", len(encoded))
	}
}
