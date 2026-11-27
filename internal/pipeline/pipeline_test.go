package pipeline

import (
	"encoding/binary"
	"testing"

	"github.com/pion/rtp"

	"rtx/internal/model"
)

// wavFixture builds a minimal PCM16 mono WAV with n frames.
func wavFixture(rate uint32, frames int) []byte {
	dataLen := uint32(frames * 2)
	buf := []byte("RIFF")
	buf = binary.LittleEndian.AppendUint32(buf, 36+dataLen)
	buf = append(buf, "WAVE"...)
	buf = append(buf, "fmt "...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = binary.LittleEndian.AppendUint32(buf, rate)
	buf = binary.LittleEndian.AppendUint32(buf, rate*2)
	buf = binary.LittleEndian.AppendUint16(buf, 2)
	buf = binary.LittleEndian.AppendUint16(buf, 16)
	buf = append(buf, "data"...)
	buf = binary.LittleEndian.AppendUint32(buf, dataLen)
	for i := 0; i < frames; i++ {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(int16(1000+i%500)))
	}
	return buf
}

// rtpFixture builds a length-framed trace of n packets, 480 ticks apart.
func rtpFixture(n int, startSeq uint16, startTS uint32) []byte {
	var out []byte
	for i := 0; i < n; i++ {
		p := rtp.Packet{Header: rtp.Header{
			Version: 2, SequenceNumber: startSeq + uint16(i),
			Timestamp: startTS + uint32(i)*480, SSRC: 1,
		}, Payload: []byte{1, 2, 3, 4}}
		raw, _ := p.Marshal()
		out = binary.BigEndian.AppendUint16(out, uint16(len(raw)))
		out = append(out, raw...)
	}
	return out
}

// mp4Fixture builds ftyp+moov(mvhd) metadata carrying the timescale.
func mp4Fixture(timescale uint32) []byte {
	mvhd := []byte{0, 0, 0, 0}                            // version+flags
	mvhd = append(mvhd, make([]byte, 8)...)               // ctime, mtime
	mvhd = binary.BigEndian.AppendUint32(mvhd, timescale) // timescale
	mvhd = binary.BigEndian.AppendUint32(mvhd, 0)         // duration
	mvhd = append(mvhd, make([]byte, 4+2+10+36+24+4)...)  // rest
	box := func(tag string, payload []byte) []byte {
		b := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload)))
		return append(append(b, tag...), payload...)
	}
	moov := box("moov", box("mvhd", mvhd))
	return append(box("ftyp", []byte("isom\x00\x00\x00\x00")), moov...)
}

func testOptions() Options {
	return Options{
		ParseOptions: ParseOptions{Limits: model.DefaultLimits(), ReordWindow: 8},
		MinQuality:   0.3,
		MaxBitrate:   1_000_000,
		TargetBPS:    128_000,
		BudgetMS:     500,
		MaxDriftPPM:  300,
	}
}

func TestEndToEndClean(t *testing.T) {
	in, err := Parse(wavFixture(48000, 9600), rtpFixture(20, 1, 1000),
		mp4Fixture(48000), testOptions().ParseOptions)
	if err != nil {
		t.Fatal(err)
	}
	a := Analyze(in, testOptions())
	if len(a.Segments) != 1 {
		t.Fatalf("want 1 segment, got %d", len(a.Segments))
	}
	if a.Drift.PPM > 50 || a.Drift.PPM < -50 {
		t.Fatalf("clean trace drift %f ppm", a.Drift.PPM)
	}
	if !a.Config.Feasible {
		t.Fatalf("clean input must be feasible: %+v", a.Config.Constraints)
	}
	v := Validate(in, a)
	for _, c := range v.Checks {
		if !c.Pass {
			t.Fatalf("check %s failed: %s", c.Name, c.Detail)
		}
	}
	if v.ValidCoverage != 1 {
		t.Fatalf("clean trace coverage %f", v.ValidCoverage)
	}
}

func TestDeterminism(t *testing.T) {
	run := func() *Analysis {
		in, err := Parse(wavFixture(48000, 9600), rtpFixture(30, 1, 0xFFFFFFF0),
			mp4Fixture(48000), testOptions().ParseOptions)
		if err != nil {
			t.Fatal(err)
		}
		return Analyze(in, testOptions())
	}
	a1, a2 := run(), run()
	if a1.BitrateBPS != a2.BitrateBPS || a1.Drift.PPM != a2.Drift.PPM ||
		a1.CostMS != a2.CostMS || len(a1.Segments) != len(a2.Segments) {
		t.Fatal("repeated runs diverge")
	}
}

func TestBudgetInfeasible(t *testing.T) {
	opts := testOptions()
	opts.BudgetMS = 1
	in, err := Parse(wavFixture(48000, 9600), rtpFixture(20, 1, 1000),
		mp4Fixture(48000), opts.ParseOptions)
	if err != nil {
		t.Fatal(err)
	}
	a := Analyze(in, opts)
	if a.Config.Feasible || !a.Config.BudgetExceeded() {
		t.Fatalf("1ms budget must be infeasible: %+v", a.Config)
	}
}
