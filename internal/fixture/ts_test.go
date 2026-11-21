package fixture

import "testing"

func TestTSPacketStructure(t *testing.T) {
	data, err := TS(4000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%188 != 0 {
		t.Fatalf("TS length %d is not a multiple of 188", len(data))
	}
	for i := 0; i < len(data); i += 188 {
		if data[i] != 0x47 {
			t.Fatalf("packet %d has sync byte %#x", i/188, data[i])
		}
	}
	if _, err := TS(0, 0); err == nil {
		t.Fatal("TS accepted zero duration")
	}
	if _, err := TS(4000, 7); err == nil {
		t.Fatal("TS accepted unaligned padding")
	}
}
