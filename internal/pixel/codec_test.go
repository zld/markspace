package pixel

import (
	"bytes"
	"math"
	"testing"
)

func gradient(w, h int, bias byte) Raw {
	r := Raw{W: w, H: h, Pix: make([]byte, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			r.Pix[i] = byte((x*7 + y*3 + int(bias)) % 256)
			r.Pix[i+1] = byte((x*2 + y*5) % 256)
			r.Pix[i+2] = byte((x + y*11) % 256)
		}
	}
	return r
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		w, h  int
		bias  byte
		track int
		seq   uint32
	}{
		{16, 16, 0, 1, 1},
		{64, 48, 9, 3, 0xffffffff},
		{97, 31, 200, 2, 77},
	} {
		src := gradient(tc.w, tc.h, tc.bias)
		b, err := Encode(src, tc.track, tc.seq)
		if err != nil {
			t.Fatalf("%dx%d encode: %v", tc.w, tc.h, err)
		}
		got, track, seq, err := Decode(b)
		if err != nil {
			t.Fatalf("%dx%d decode: %v", tc.w, tc.h, err)
		}
		if track != tc.track || seq != tc.seq {
			t.Fatalf("ids = %d/%d", track, seq)
		}
		if !bytes.Equal(got.Pix, src.Pix) {
			t.Fatalf("%dx%d planes differ", tc.w, tc.h)
		}
	}
}

func TestEncodeRunsCompress(t *testing.T) {
	r := Raw{W: 64, H: 64, Pix: make([]byte, 64*64*3)}
	for i := range r.Pix {
		r.Pix[i] = 0x5a
	}
	b, err := Encode(r, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) >= 64*64*3/10 {
		t.Fatalf("flat frame did not compress: %d bytes", len(b))
	}
	out, _, _, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Pix, r.Pix) {
		t.Fatal("flat frame round trip differs")
	}
}

func TestDecodeRejectsCorruption(t *testing.T) {
	src := gradient(32, 32, 0)
	b, err := Encode(src, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	flip := append([]byte(nil), b...)
	flip[len(flip)/2] ^= 0xff
	if _, _, _, err := Decode(flip); err == nil {
		t.Fatal("corrupted payload accepted")
	}
	if _, _, _, err := Decode(b[:HeaderSize-1]); err == nil {
		t.Fatal("truncated header accepted")
	}
	if _, _, _, err := Decode(b[:len(b)-3]); err == nil {
		t.Fatal("truncated body accepted")
	}
	trailing := append(append([]byte(nil), b...), 0)
	if _, _, _, err := Decode(trailing); err == nil {
		t.Fatal("trailing bytes accepted")
	}
}

func TestMeasureExactValues(t *testing.T) {
	r := Raw{W: 16, H: 16, Pix: make([]byte, 16*16*3)}
	for i := 0; i < 256; i++ {
		r.Pix[i*3] = 10
		r.Pix[i*3+1] = 20
		r.Pix[i*3+2] = 30
	}
	st, err := Measure(r)
	if err != nil {
		t.Fatal(err)
	}
	if st.MeanR != 10 || st.MeanG != 20 || st.MeanB != 30 {
		t.Fatalf("means = %v", st)
	}
	if math.Abs(st.Variance-200.0/3) > 1e-6 {
		t.Fatalf("variance = %f", st.Variance)
	}
	if st.Min != 10 || st.Max != 30 {
		t.Fatalf("range = %d..%d", st.Min, st.Max)
	}
}

func TestQuantiseBounds(t *testing.T) {
	r := gradient(24, 24, 0)
	q := Quantise(r, 16)
	for _, v := range q.Pix {
		if int(v)%16 != 0 {
			t.Fatalf("sample %d not quantised", v)
		}
	}
	if q.W != r.W || q.H != r.H {
		t.Fatal("geometry changed")
	}
	for _, step := range []int{1, 8, 64, 255} {
		if got := StepForQuality(Quality(step)); got != step {
			t.Fatalf("quality round trip step %d -> %d", step, got)
		}
	}
	if Quality(1) != 1 || Quality(255) < 0 || Quality(255) > 0.02 {
		t.Fatalf("quality bounds wrong: %f %f", Quality(1), Quality(255))
	}
}
