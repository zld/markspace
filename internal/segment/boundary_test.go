package segment

import (
	"testing"

	"mediacheck/internal/clock"
	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

// stream builds a byte stream of 4-byte-start-code units with the
// given payload sizes; keyframes at the given unit indexes.
func stream(sizes []int, keys map[int]bool) ([]byte, []codec.Unit, []clock.Ticks) {
	var data []byte
	var units []codec.Unit
	var ticks []clock.Ticks
	for i, sz := range sizes {
		sc := int64(len(data))
		data = append(data, 0, 0, 0, 1)
		hdr := int64(len(data))
		data = append(data, 0x41)
		for j := 0; j < sz; j++ {
			data = append(data, byte(i*16+j))
		}
		units = append(units, codec.Unit{
			Offset: hdr, StartCode: sc, Size: 1 + sz,
			Name: "slice", Keyframe: keys[i],
		})
		ticks = append(ticks, clock.Ticks(i*3000))
	}
	return data, units, ticks
}

func TestCutAtKeyframes(t *testing.T) {
	keys := map[int]bool{0: true, 3: true, 6: true}
	data, units, ticks := stream([]int{10, 10, 10, 10, 10, 10, 10, 10}, keys)
	segs, err := Cut(units, ticks, 9000, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 3 {
		t.Fatalf("got %d segments", len(segs))
	}
	for i, s := range segs {
		if s.Index != i || !s.Units[0].Keyframe {
			t.Fatalf("segment %d starts at non-keyframe", i)
		}
		if len(s.SHA256) != 64 {
			t.Fatalf("hash %q not 64 hex chars", s.SHA256)
		}
	}
	if segs[0].StartTick != 0 || segs[1].StartTick != 9000 || segs[2].StartTick != 18000 {
		t.Fatalf("ticks: %+v", segs)
	}
	// deterministic: same input, same hashes
	again, _ := Cut(units, ticks, 9000, data)
	for i := range segs {
		if segs[i].SHA256 != again[i].SHA256 {
			t.Fatal("hashes not reproducible")
		}
	}
}

func TestCutRejectsBadSlices(t *testing.T) {
	data, units, ticks := stream([]int{10, 10}, map[int]bool{0: true})
	if _, err := Cut(nil, nil, 9000, data); !fault.HasCode(err, fault.ErrSliceOutOfRange) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := Cut(units, ticks[:1], 9000, data); !fault.HasCode(err, fault.ErrSliceOutOfRange) {
		t.Fatalf("tick mismatch: %v", err)
	}
	if _, err := Cut(units, ticks, 0, data); !fault.HasCode(err, fault.ErrSliceOutOfRange) {
		t.Fatalf("zero segTicks: %v", err)
	}
	units[0].Keyframe = false
	if _, err := Cut(units, ticks, 9000, data); !fault.HasCode(err, fault.ErrSliceOutOfRange) {
		t.Fatalf("non-keyframe start: %v", err)
	}
}

func TestCheckRTP(t *testing.T) {
	payload := make([]byte, 1000)
	for i := range payload {
		payload[i] = byte(i)
	}
	n, err := CheckRTP(payload, 96, 0x11223344, 90000, 100, 300)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 { // 300+300+300+100
		t.Fatalf("got %d packets", n)
	}
	if _, err := CheckRTP(nil, 96, 1, 1, 1, 300); !fault.HasCode(err, fault.ErrRTPBoundary) {
		t.Fatalf("empty payload: %v", err)
	}
	if _, err := CheckRTP(payload, 96, 1, 1, 1, 0); !fault.HasCode(err, fault.ErrRTPBoundary) {
		t.Fatalf("zero mtu: %v", err)
	}
}
