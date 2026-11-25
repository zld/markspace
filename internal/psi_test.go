package kernel

import "testing"

func TestPSIEncodedPATAndPMTApply(t *testing.T) {
	pat := EncodePATSection(1, 0x1000)
	pmt := EncodePMTSection(2, 0x0100, []PMTStream{
		{PID: 0x0100, StreamType: 0x1b},
		{PID: 0x0101, StreamType: 0x0f},
	})
	tr := NewPSITracker()
	if _, err := tr.HandleTSPayload(0, TSPayloadWithPointer(pat)); err != nil {
		t.Fatal(err)
	}
	m, err := tr.HandleTSPayload(0x1000, TSPayloadWithPointer(pmt))
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.PMTVersion != 2 || m.TrackPIDs[0x0101] != TrackAudio {
		t.Fatalf("bad map: %#v", m)
	}
	if !tr.CuttingEnabled(0x0100) || !tr.CuttingEnabled(0x0101) {
		t.Fatal("mapped tracks should be enabled")
	}
}

func TestPSIIncompleteVersionRetainsOldMapAndPausesTracks(t *testing.T) {
	tr := NewPSITracker()
	old := EncodePMTSection(1, 0x0100, []PMTStream{{PID: 0x0101, StreamType: 0x0f}})
	if _, err := tr.HandleSection(0x1000, mustParseSection(t, old)); err != nil {
		t.Fatal(err)
	}
	if tr.Current.PMTVersion != 1 {
		t.Fatal("old map not applied")
	}

	first := &PSISection{
		TableID: 0x02, Version: 2, CurrentNext: true,
		SectionNumber: 0, LastSectionNumber: 1,
		PCRPID: 0x0100, Streams: map[uint16]uint8{0x0100: 0x1b},
	}
	if m, err := tr.HandleSection(0x1000, first); err != nil {
		t.Fatal(err)
	} else if m.PMTVersion != 1 {
		t.Fatal("old map must remain effective before completion")
	}
	if !tr.Incomplete() || tr.CuttingEnabled(0x0101) {
		t.Fatal("affected old track should be paused during incomplete update")
	}

	second := &PSISection{
		TableID: 0x02, Version: 2, CurrentNext: true,
		SectionNumber: 1, LastSectionNumber: 1,
		PCRPID: 0x0100, Streams: map[uint16]uint8{0x0101: 0x0f},
	}
	if _, err := tr.HandleSection(0x1000, second); err != nil {
		t.Fatal(err)
	}
	if tr.Incomplete() || !tr.CuttingEnabled(0x0101) || tr.Current.PMTVersion != 2 {
		t.Fatalf("update did not complete: incomplete=%v current=%#v", tr.Incomplete(), tr.Current)
	}
}

func mustParseSection(t *testing.T, b []byte) *PSISection {
	t.Helper()
	s, err := ParseSection(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
