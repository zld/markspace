// Package tests verifies the rebuild / slice / recovery behaviour of
// the whole pipeline against generated fixtures.
package tests

import (
	"bytes"
	"testing"

	"tsaudit/internal/fixture"
	"tsaudit/internal/psi"
	"tsaudit/internal/slicer"
)

func run(t *testing.T, data []byte) *slicer.Result {
	t.Helper()
	res, err := slicer.Run(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	return res
}

// counts returns interval -> pid -> packets for mapped streams.
func counts(res *slicer.Result) []map[uint16]int {
	out := make([]map[uint16]int, len(res.Intervals))
	for i, iv := range res.Intervals {
		out[i] = iv.Counts
	}
	return out
}

func TestRemapScenario(t *testing.T) {
	res := run(t, fixture.Remap())
	if !res.HasTables {
		t.Fatal("expected valid tables")
	}
	if res.PATChanges != 1 || res.PMTChanges != 1 {
		t.Fatalf("version changes: PAT %d PMT %d, want 1/1", res.PATChanges, res.PMTChanges)
	}
	if len(res.Intervals) != 2 {
		t.Fatalf("intervals: %d, want 2", len(res.Intervals))
	}
	if got := res.Intervals[0].Snapshot.EffectivePacket; got != 1 {
		t.Fatalf("interval 0 effective at packet %d, want 1", got)
	}
	if got := res.Intervals[1].Snapshot.EffectivePacket; got != 11 {
		t.Fatalf("interval 1 effective at packet %d, want 11", got)
	}
	want := []map[uint16]int{
		{fixture.VideoPID: 4, fixture.AudioPIDOld: 4},
		{fixture.VideoPID: 3, fixture.AudioPIDNew: 5},
	}
	for i, c := range counts(res) {
		for pid, n := range want[i] {
			if c[pid] != n {
				t.Errorf("interval %d pid %d: %d packets, want %d", i, pid, c[pid], n)
			}
		}
	}
	if got := res.Intervals[1].Unmapped[fixture.AudioPIDOld]; got != 2 {
		t.Fatalf("old audio pid unmapped in interval 1: %d, want 2", got)
	}
	if len(res.Incomplete) != 0 || res.CorruptedSections != 0 {
		t.Fatalf("unexpected anomalies: incomplete %v corrupted %d", res.Incomplete, res.CorruptedSections)
	}
}

func TestRemapWithGapScenario(t *testing.T) {
	res := run(t, fixture.RemapWithGap())
	if res.PATChanges != 1 || res.PMTChanges != 1 {
		t.Fatalf("version changes: PAT %d PMT %d, want 1/1", res.PATChanges, res.PMTChanges)
	}
	if len(res.Intervals) != 2 {
		t.Fatalf("intervals: %d, want 2", len(res.Intervals))
	}
	// The new mapping takes effect at the packet completing the table.
	if got := res.Intervals[1].Snapshot.EffectivePacket; got != 12 {
		t.Fatalf("interval 1 effective at packet %d, want 12", got)
	}
	if got := res.Intervals[1].Counts[fixture.AudioPIDNew]; got != 5 {
		t.Fatalf("new audio pid: %d packets, want 5", got)
	}
	st := res.Continuity[fixture.VideoPID]
	if st.Gaps != 1 || st.MissingPackets != 1 {
		t.Fatalf("video continuity: %+v, want 1 gap / 1 missing", st)
	}
	// The gap is attributed to the interval in effect at detection.
	a := res.Intervals[1].Anomalies[fixture.VideoPID]
	if a == nil || a.Gaps != 1 {
		t.Fatalf("interval 1 video anomalies: %+v, want 1 gap", a)
	}
}

func TestIncompleteUpdateRetainsMapping(t *testing.T) {
	b := fixture.NewBuilder()
	b.Section(psi.PATPID, fixture.PATSection(0, map[uint16]uint16{fixture.ProgramNumber: fixture.PMTPID}))
	b.Section(fixture.PMTPID, fixture.PMTSection(0, fixture.ProgramNumber,
		[]fixture.Stream{{Type: fixture.StreamTypeVideo, PID: fixture.VideoPID}, {Type: fixture.StreamTypeAudio, PID: fixture.AudioPIDOld}}))
	b.Media(fixture.VideoPID, 2)
	// New PMT version starts but only its first fragment ever arrives.
	frag := fixture.PMTSection(1, fixture.ProgramNumber,
		[]fixture.Stream{{Type: fixture.StreamTypeVideo, PID: fixture.VideoPID}, {Type: fixture.StreamTypeAudio, PID: fixture.AudioPIDNew}})[:10]
	b.Packet(fixture.PMTPID, true, append([]byte{0x00}, frag...)) // pointer_field = 0, then 10 of 26 bytes
	b.Media(fixture.VideoPID, 2)
	b.Media(fixture.AudioPIDNew, 3) // not yet mapped: old table still in effect

	res := run(t, b.Bytes())
	if !res.HasTables {
		t.Fatal("old tables must stay valid")
	}
	if len(res.Intervals) != 1 {
		t.Fatalf("intervals: %d, want 1 (old mapping retained)", len(res.Intervals))
	}
	if res.PMTChanges != 0 {
		t.Fatalf("pmt changes: %d, want 0", res.PMTChanges)
	}
	if got := res.Intervals[0].Counts[fixture.VideoPID]; got != 4 {
		t.Fatalf("video packets: %d, want 4", got)
	}
	if got := res.Intervals[0].Unmapped[fixture.AudioPIDNew]; got != 3 {
		t.Fatalf("new audio unmapped: %d, want 3", got)
	}
	if len(res.Incomplete) != 1 {
		t.Fatalf("incomplete updates: %v, want exactly 1", res.Incomplete)
	}
	inc := res.Incomplete[0]
	if inc.Table != "PMT" || inc.PID != fixture.PMTPID || inc.Version != 1 {
		t.Fatalf("incomplete update: %+v, want PMT pid 4096 version 1", inc)
	}
	if inc.MissingPackets < 1 {
		t.Fatalf("missing packets: %d, want >= 1", inc.MissingPackets)
	}
}

func TestCorruptSectionRetainsMapping(t *testing.T) {
	b := fixture.NewBuilder()
	b.Section(psi.PATPID, fixture.PATSection(0, map[uint16]uint16{fixture.ProgramNumber: fixture.PMTPID}))
	b.Section(fixture.PMTPID, fixture.PMTSection(0, fixture.ProgramNumber,
		[]fixture.Stream{{Type: fixture.StreamTypeVideo, PID: fixture.VideoPID}, {Type: fixture.StreamTypeAudio, PID: fixture.AudioPIDOld}}))
	b.Media(fixture.VideoPID, 2)
	corrupt := fixture.PMTSection(1, fixture.ProgramNumber,
		[]fixture.Stream{{Type: fixture.StreamTypeVideo, PID: fixture.VideoPID}, {Type: fixture.StreamTypeAudio, PID: fixture.AudioPIDNew}})
	corrupt[12] ^= 0xFF // break the CRC
	b.Section(fixture.PMTPID, corrupt)
	b.Media(fixture.VideoPID, 2)

	res := run(t, b.Bytes())
	if res.CorruptedSections != 1 {
		t.Fatalf("corrupted sections: %d, want 1", res.CorruptedSections)
	}
	if len(res.Intervals) != 1 || res.PMTChanges != 0 {
		t.Fatalf("mapping must stay at v0: intervals %d changes %d", len(res.Intervals), res.PMTChanges)
	}
	if got := res.Intervals[0].Counts[fixture.VideoPID]; got != 4 {
		t.Fatalf("video packets: %d, want 4", got)
	}
}

func TestNoValidTables(t *testing.T) {
	b := fixture.NewBuilder()
	b.Media(fixture.VideoPID, 5)
	res := run(t, b.Bytes())
	if res.HasTables {
		t.Fatal("stream without PAT/PMT must report no valid tables")
	}
	if got := res.Pre.Unmapped[fixture.VideoPID]; got != 5 {
		t.Fatalf("pre-table unmapped: %d, want 5", got)
	}
}

func TestDuplicateSectionsIgnored(t *testing.T) {
	b := fixture.NewBuilder()
	pat := fixture.PATSection(0, map[uint16]uint16{fixture.ProgramNumber: fixture.PMTPID})
	pmt := fixture.PMTSection(0, fixture.ProgramNumber,
		[]fixture.Stream{{Type: fixture.StreamTypeVideo, PID: fixture.VideoPID}, {Type: fixture.StreamTypeAudio, PID: fixture.AudioPIDOld}})
	b.Section(psi.PATPID, pat)
	b.Section(psi.PATPID, pat) // duplicate
	b.Section(fixture.PMTPID, pmt)
	b.Section(fixture.PMTPID, pmt) // duplicate
	b.Media(fixture.VideoPID, 2)

	res := run(t, b.Bytes())
	if res.PATChanges != 0 || res.PMTChanges != 0 {
		t.Fatalf("duplicates must not count as changes: PAT %d PMT %d", res.PATChanges, res.PMTChanges)
	}
	if len(res.Intervals) != 1 {
		t.Fatalf("intervals: %d, want 1", len(res.Intervals))
	}
}

func TestVersionWrap(t *testing.T) {
	b := fixture.NewBuilder()
	b.Section(psi.PATPID, fixture.PATSection(0, map[uint16]uint16{fixture.ProgramNumber: fixture.PMTPID}))
	pmt := func(version int, audio uint16) {
		b.Section(fixture.PMTPID, fixture.PMTSection(version, fixture.ProgramNumber,
			[]fixture.Stream{{Type: fixture.StreamTypeVideo, PID: fixture.VideoPID}, {Type: fixture.StreamTypeAudio, PID: audio}}))
	}
	pmt(0, fixture.AudioPIDOld)
	b.Media(fixture.AudioPIDOld, 1)
	pmt(31, fixture.AudioPIDNew)
	b.Media(fixture.AudioPIDNew, 1)
	pmt(0, 259) // version wrapped 31 -> 0
	b.Media(259, 1)

	res := run(t, b.Bytes())
	if res.PMTChanges != 2 {
		t.Fatalf("pmt changes: %d, want 2", res.PMTChanges)
	}
	if len(res.Intervals) != 3 {
		t.Fatalf("intervals: %d, want 3", len(res.Intervals))
	}
	for i, audio := range []uint16{fixture.AudioPIDOld, fixture.AudioPIDNew, 259} {
		if got := res.Intervals[i].Counts[audio]; got != 1 {
			t.Errorf("interval %d audio pid %d: %d packets, want 1", i, audio, got)
		}
	}
}

func TestMultiSectionMultiProgram(t *testing.T) {
	const audioPID2 = 300
	b := fixture.NewBuilder()
	// PAT v0 split across two section numbers, delivered out of order.
	b.Section(psi.PATPID, fixture.Resection(fixture.PATSection(0, map[uint16]uint16{2: 4097}), 1, 1))
	b.Media(fixture.VideoPID, 1) // no complete PAT yet: unmapped
	b.Section(psi.PATPID, fixture.Resection(fixture.PATSection(0, map[uint16]uint16{fixture.ProgramNumber: fixture.PMTPID}), 0, 1))
	b.Section(fixture.PMTPID, fixture.PMTSection(0, fixture.ProgramNumber,
		[]fixture.Stream{{Type: fixture.StreamTypeVideo, PID: fixture.VideoPID}}))
	b.Section(4097, fixture.PMTSection(0, 2, []fixture.Stream{{Type: fixture.StreamTypeAudio, PID: audioPID2}}))
	b.Media(fixture.VideoPID, 2)
	b.Media(audioPID2, 3)

	res := run(t, b.Bytes())
	// Each PMT completes at its own packet: the first PMT opens an
	// interval with only the video stream, the second PMT's completion
	// opens the final two-program mapping.
	if len(res.Intervals) != 2 {
		t.Fatalf("intervals: %d, want 2", len(res.Intervals))
	}
	snap := res.Intervals[1].Snapshot
	if len(snap.Streams) != 2 {
		t.Fatalf("mapped streams: %v, want 2", snap.Streams)
	}
	if got := res.Intervals[1].Counts[fixture.VideoPID]; got != 2 {
		t.Fatalf("video packets: %d, want 2", got)
	}
	if got := res.Intervals[1].Counts[audioPID2]; got != 3 {
		t.Fatalf("program 2 audio packets: %d, want 3", got)
	}
	if got := res.Pre.Unmapped[fixture.VideoPID]; got != 1 {
		t.Fatalf("pre-table video packets: %d, want 1", got)
	}
	if snap.PMTVersions[fixture.ProgramNumber] != 0 || snap.PMTVersions[2] != 0 {
		t.Fatalf("pmt versions: %v", snap.PMTVersions)
	}
}

func TestContinuityDuplicateAndTransportError(t *testing.T) {
	b := fixture.NewBuilder()
	b.Section(psi.PATPID, fixture.PATSection(0, map[uint16]uint16{fixture.ProgramNumber: fixture.PMTPID}))
	b.Section(fixture.PMTPID, fixture.PMTSection(0, fixture.ProgramNumber,
		[]fixture.Stream{{Type: fixture.StreamTypeVideo, PID: fixture.VideoPID}}))
	b.Media(fixture.VideoPID, 5)
	data := b.Bytes()
	setCC := func(pkt int, cc byte) {
		data[pkt*188+3] = data[pkt*188+3]&0xF0 | cc&0x0F
	}
	// Media packets are 2..6; force sequence 0,0(dup),1,2(TE),3.
	setCC(3, 0)
	setCC(4, 1)
	setCC(5, 2)
	data[5*188+1] |= 0x80 // transport error indicator
	setCC(6, 3)

	res := run(t, data)
	st := res.Continuity[fixture.VideoPID]
	if st.Duplicates != 1 || st.TransportErrors != 1 || st.Gaps != 0 {
		t.Fatalf("continuity: %+v, want 1 duplicate, 1 transport error, 0 gaps", st)
	}
	if got := res.Intervals[0].Counts[fixture.VideoPID]; got != 5 {
		t.Fatalf("video packets: %d, want 5 (anomalies never drop packets)", got)
	}
}
