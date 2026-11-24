package clock

import "testing"

func TestTimelineWrapIsMonotonic(t *testing.T) {
	tl, err := NewTimeline(StreamAudio, 42, 48000, 1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	prev := tl.Map(0xfffffff0)
	for ts := uint32(0xfffffff1); ; ts++ {
		got := tl.Map(ts)
		if got < prev {
			t.Fatalf("time went backwards at ts=%x: %d < %d", ts, got, prev)
		}
		prev = got
		if ts == 0x10 {
			break
		}
	}
	// From fffffff0 to 0x10 across the wrap is 32 timestamp ticks.
	want := int64(32) * 1_000_000_000 / 48000
	if got := prev - 1_000_000_000; got != want {
		t.Fatalf("wrap mapping = %dns, want %dns", got, want)
	}
}

func TestUnwrap32(t *testing.T) {
	cases := []struct {
		ref   uint64
		value uint32
		want  int64
	}{
		{0xffffffff, 0, 0x100000000},
		{100, 90, 90},
		{0x10000000a, 0xffffffff, 0xffffffff},
	}
	for _, tc := range cases {
		if got := Unwrap32(tc.ref, tc.value); got != tc.want {
			t.Fatalf("Unwrap32(%x,%x)=%x want %x", tc.ref, tc.value, got, tc.want)
		}
	}
}

func TestRTCPReportCorrectsMapping(t *testing.T) {
	tl, _ := NewTimeline(StreamVideo, 7, 90000, 0)
	tl.Map(90000) // one second from origin
	report := RTCPReport{SSRC: 7, NTPNS: 2_000_000_000, RTPTimestamp: 180000}
	correction, err := tl.ApplyRTCP(report)
	if err != nil {
		t.Fatal(err)
	}
	// Old mapping was 2s at RTP 180000 too? origin first at ts? First Map(90000) maps origin 0 at ts90000, then 180000 maps 1s. So correction +1s.
	if correction != 1_000_000_000 {
		t.Fatalf("correction=%d", correction)
	}
	if got := tl.Map(180000); got != 2_000_000_000 {
		t.Fatalf("corrected mapping=%d", got)
	}
}

func TestComparableErrorModel(t *testing.T) {
	a := MediaError{Code: CodeMissingPacket, Stream: StreamAudio, Sequence: 4, InputOffset: 128}
	b := a
	if a != b {
		t.Fatal("MediaError must be directly comparable")
	}
	if a.Error() == "" {
		t.Fatal("Error() empty")
	}
}
