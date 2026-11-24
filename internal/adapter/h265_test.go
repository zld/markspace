package adapter

import (
	"bytes"
	"testing"
)

func TestH265Classification(t *testing.T) {
	cases := []struct {
		name               string
		hdr                byte
		wantType           int
		wantFrame, wantKey bool
	}{
		{"IDR_W_RADL", 0x26, h265TypeIDRWRADL, true, true},
		{"IDR_N_LP", 0x28, h265TypeIDRNLP, true, true},
		{"TRAIL_R", 0x02, 1, true, false},
		{"SPS", 0x42, h265TypeSPS, false, false},
		{"PPS", 0x44, h265TypePPS, false, false},
	}
	for _, tc := range cases {
		got, frame, key := classifyH265([]byte{tc.hdr, 0x01})
		if got != tc.wantType || frame != tc.wantFrame || key != tc.wantKey {
			t.Errorf("%s: got (%d,%v,%v) want (%d,%v,%v)",
				tc.name, got, frame, key, tc.wantType, tc.wantFrame, tc.wantKey)
		}
	}
}

func TestH265SplitterStream(t *testing.T) {
	stream := bytes.Join([][]byte{
		buildNAL([]byte{0, 0, 0, 1}, 0x40, []byte{0x01}),            // VPS
		buildNAL([]byte{0, 0, 0, 1}, 0x42, []byte{0x02, 0x03}),      // SPS
		buildNAL([]byte{0, 0, 1}, 0x44, []byte{0x04}),               // PPS
		buildNAL([]byte{0, 0, 1}, 0x26, bytes.Repeat([]byte{9}, 6)), // IDR
		buildNAL([]byte{0, 0, 1}, 0x02, bytes.Repeat([]byte{8}, 6)), // TRAIL
	}, nil)

	a, err := Get("h265", H265Version)
	if err != nil {
		t.Fatal(err)
	}
	sp := a.NewSplitter()
	units, uerrs, err := sp.Write(stream)
	if err != nil || len(uerrs) != 0 {
		t.Fatalf("unexpected errors: %v %v", err, uerrs)
	}
	tail, dangling := sp.Close()
	units = append(units, tail...)
	if dangling != nil {
		t.Fatal(dangling)
	}
	if len(units) != 5 {
		t.Fatalf("want 5 units, got %d", len(units))
	}
	if units[3].TypeCode != h265TypeIDRWRADL || !units[3].IsKeyframe {
		t.Fatalf("unit 3 must be IDR keyframe: %+v", units[3])
	}
	if !units[4].IsFrame || units[4].IsKeyframe {
		t.Fatalf("unit 4 must be non-key VCL: %+v", units[4])
	}
	if H265ParameterSet(units[0]) && H265ParameterSet(units[2]) {
		return
	}
	t.Fatal("VPS/PPS parameter-set detection failed")
}

func TestH265ByteAtATimeInvariant(t *testing.T) {
	stream := bytes.Join([][]byte{
		buildNAL([]byte{0, 0, 0, 1}, 0x26, []byte{1, 2}),
		buildNAL([]byte{0, 0, 0, 1}, 0x02, []byte{3, 4}),
		buildNAL([]byte{0, 0, 1}, 0x28, []byte{5}),
	}, nil)
	sp := NewAnnexBScanner(classifyH265, h265MaxUnit)
	var n int
	var keys int
	for i := 0; i < len(stream); i++ {
		units, _, err := sp.Write(stream[i : i+1])
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range units {
			n++
			if u.IsKeyframe {
				keys++
			}
		}
	}
	tail, dangling := sp.Close()
	if dangling != nil {
		t.Fatal(dangling)
	}
	for _, u := range tail {
		n++
		if u.IsKeyframe {
			keys++
		}
	}
	if n != 3 || keys != 2 {
		t.Fatalf("want 3 units / 2 keyframes, got %d / %d", n, keys)
	}
}
