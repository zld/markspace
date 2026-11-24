package demux

import (
	"testing"

	"medpipe/internal/clock"
	"medpipe/internal/fault"
	"medpipe/internal/samplegen"
)

func TestDemuxCleanGeneratedTS(t *testing.T) {
	spec := samplegen.TSSpec{IncludePCR: true}
	for i := 0; i < 6; i++ {
		spec.Video = append(spec.Video, samplegen.SampleSpec{PTS: uint64(i) * 3600, Data: []byte{0x00, 0x00, 0x01, byte(i + 1), 0xaa, 0xbb}})
		spec.Audio = append(spec.Audio, samplegen.SampleSpec{PTS: uint64(i) * 1800, Data: []byte{0xff, 0xf1, byte(i + 1), 0xcc}})
	}
	data := samplegen.BuildTS(spec)
	m, events, err := DemuxTS(data)
	if err != nil {
		t.Fatalf("DemuxTS returned error: %v events=%+v", err, events)
	}
	if len(m.Tracks) != 2 {
		t.Fatalf("tracks=%d want 2", len(m.Tracks))
	}
	for _, tr := range m.Tracks {
		if len(tr.Samples) != 6 {
			t.Fatalf("track %d samples=%d want 6", tr.PID, len(tr.Samples))
		}
		if tr.Samples[1].PTSMS <= tr.Samples[0].PTSMS {
			t.Fatalf("non-monotonic samples: %+v", tr.Samples[:2])
		}
	}
}

func TestPCRAndPTSWrapProduceLocatedEvents(t *testing.T) {
	spec := samplegen.TSSpec{IncludePCR: true}
	spec.Video = []samplegen.SampleSpec{
		{PTS: clock.Mod33 - 3600, Data: []byte{0, 0, 1, 1, 0xaa}},
		{PTS: 3600, Data: []byte{0, 0, 1, 2, 0xbb}},
	}
	m, events, err := DemuxTS(samplegen.BuildTS(spec))
	if err != nil {
		t.Fatal(err)
	}
	codes := map[fault.Code]bool{}
	for _, e := range events {
		codes[e.Code] = true
		if (e.Code == fault.ErrPCRWrap || e.Code == fault.ErrPTSWrap) && e.PID == 0 {
			t.Fatalf("wrap event missing PID: %+v", e)
		}
	}
	if !codes[fault.ErrPCRWrap] || !codes[fault.ErrPTSWrap] {
		t.Fatalf("missing wrap events: %+v", events)
	}
	if len(m.Tracks[0].Samples) != 2 || m.Tracks[0].Samples[1].PTSMS <= m.Tracks[0].Samples[0].PTSMS {
		t.Fatalf("wrapped timestamps not monotonic: %+v", m.Tracks[0].Samples)
	}
}
