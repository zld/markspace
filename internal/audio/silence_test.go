package audio

import (
	"testing"

	"github.com/pion/rtp"
)

func tone(n int, amp int16) []int16 {
	out := make([]int16, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = amp
		} else {
			out[i] = -amp
		}
	}
	return out
}

func TestAnalyzePCMFindsExactSilenceWindows(t *testing.T) {
	// 48 kHz mono: 1 s tone, 0.5 s silence, 1 s tone.
	rate := 48000
	var pcm []int16
	pcm = append(pcm, tone(rate, 8000)...)
	pcm = append(pcm, make([]int16, rate/2)...)
	pcm = append(pcm, tone(rate, 8000)...)
	res, err := AnalyzePCM(1, pcm, 1, rate, 0, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
	if !res.Detectable || len(res.Silence) != 1 {
		t.Fatalf("detectable=%v silence=%v", res.Detectable, res.Silence)
	}
	s := res.Silence[0]
	if s.Start < 990_000_000 || s.Start > 1_010_000_000 {
		t.Fatalf("silence start %d outside 1s tolerance", s.Start)
	}
	if d := s.End - s.Start; d < 480_000_000 || d > 520_000_000 {
		t.Fatalf("silence duration %d", d)
	}
	if res.IsSilent(1_200_000_000) != true || res.IsSilent(0) != false {
		t.Fatal("IsSilent disagreed with range")
	}
}

func TestAnalyzePCMShortSilenceSuppressed(t *testing.T) {
	rate := 48000
	var pcm []int16
	pcm = append(pcm, tone(rate, 8000)...)
	pcm = append(pcm, make([]int16, rate/20)...) // 50 ms
	pcm = append(pcm, tone(rate, 8000)...)
	res, _ := AnalyzePCM(1, pcm, 1, rate, 0, DefaultConfig())
	if len(res.Silence) != 0 {
		t.Fatalf("50ms dip should be suppressed, got %v", res.Silence)
	}
}

func TestStereoInterleavedAveraging(t *testing.T) {
	// Left silent, right loud -> not silent at -60 dBFS threshold.
	rate := 8000
	mono := tone(rate, 6000)
	pcm := make([]int16, 0, rate*2)
	for _, v := range mono {
		pcm = append(pcm, 0, v)
	}
	res, _ := AnalyzePCM(3, pcm, 2, rate, 0, DefaultConfig())
	if len(res.Silence) != 0 {
		t.Fatalf("one loud channel must defeat silence: %v", res.Silence)
	}
}

func TestMissingAudioIsUndetectableNotSilent(t *testing.T) {
	cases := []struct {
		name string
		run  func() (Result, error)
	}{
		{"no pcm", func() (Result, error) { return AnalyzePCM(1, nil, 1, 48000, 0, DefaultConfig()) }},
		{"zero rate", func() (Result, error) { return AnalyzePCM(1, tone(10, 100), 1, 0, 0, DefaultConfig()) }},
		{"no packets", func() (Result, error) { return AnalyzeRTP(1, nil, 48000, DefaultConfig()) }},
		{"no extension", func() (Result, error) {
			pk := make([]rtp.Packet, 5)
			for i := range pk {
				pk[i].Timestamp = uint32(i * 960)
			}
			return AnalyzeRTP(1, pk, 48000, DefaultConfig())
		}},
	}
	for _, tc := range cases {
		res, err := tc.run()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Detectable || len(res.Silence) != 0 {
			t.Fatalf("%s: must be undetectable without silence claims: %+v", tc.name, res)
		}
		if err := res.Validate(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

func TestRTPAudioLevelSilence(t *testing.T) {
	cfg := DefaultConfig()
	var packets []rtp.Packet
	levels := []uint8{10, 90, 95, 92, 8}
	for i, lv := range levels {
		p := rtp.Packet{Header: rtp.Header{Timestamp: uint32(i * 4800)}} // 100 ms spacing
		ext, err := rtp.AudioLevelExtension{Level: lv}.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := p.SetExtension(cfg.LevelID, ext); err != nil {
			t.Fatal(err)
		}
		packets = append(packets, p)
	}
	res, err := AnalyzeRTP(7, packets, 48000, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detectable || len(res.Silence) != 1 {
		t.Fatalf("detectable=%v silence=%v", res.Detectable, res.Silence)
	}
	// Wrap in RTP marshal/unmarshal to prove the extension survives the wire.
	raw, err := packets[1].Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back rtp.Packet
	if err := back.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	var lv rtp.AudioLevelExtension
	if err := lv.Unmarshal(back.GetExtension(cfg.LevelID)); err != nil || lv.Level != 90 {
		t.Fatalf("roundtrip level=%d err=%v", lv.Level, err)
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsUndetectableSilence(t *testing.T) {
	res := Result{Detectable: false, Silence: []Range{{0, 1}}}
	if err := res.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}
