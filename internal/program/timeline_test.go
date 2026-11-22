package program

import (
	"testing"

	"tsrebuild/internal/psi"
)

func pat(version uint8, programs []psi.PATProgram) *psi.PAT {
	return &psi.PAT{TransportStreamID: 9, Version: version, NetworkPID: 0x1fff, Programs: programs}
}

func pmt(programNumber, pcrPID, version uint16, streams []psi.ElementaryStream) *psi.PMT {
	return &psi.PMT{ProgramNumber: programNumber, PCRPID: pcrPID, Version: uint8(version), Streams: streams}
}

func video(pid uint16) psi.ElementaryStream {
	return psi.ElementaryStream{PID: pid, StreamType: 0x1b, Kind: psi.StreamVideo}
}
func audio(pid uint16) psi.ElementaryStream {
	return psi.ElementaryStream{PID: pid, StreamType: 0x0f, Kind: psi.StreamAudio}
}

func TestTimelinePATVersionChanges(t *testing.T) {
	tl := NewTimeline(9)
	progs := []psi.PATProgram{{ProgramNumber: 1, PMTPID: 100}}

	tl.ApplyPAT(pat(0, progs))
	if tl.PATVersionChanges() != 0 {
		t.Fatal("first PAT must not count as change")
	}
	tl.ApplyPAT(pat(0, progs)) // repeated table
	if tl.PATVersionChanges() != 0 {
		t.Fatal("duplicate PAT version must not count as change")
	}
	tl.ApplyPAT(pat(1, progs))
	if tl.PATVersionChanges() != 1 {
		t.Fatalf("expected 1 PAT change, got %d", tl.PATVersionChanges())
	}
	tl.ApplyPAT(pat(31, progs)) // 5-bit wrap
	if tl.PATVersionChanges() != 2 {
		t.Fatalf("version wrap must count as change, got %d", tl.PATVersionChanges())
	}
}

func TestTimelineIntervalsOnRemap(t *testing.T) {
	tl := NewTimeline(9)
	tl.ApplyPAT(pat(0, []psi.PATProgram{{ProgramNumber: 1, PMTPID: 100}}))

	iv0, err := tl.ApplyMapping(1, 100, 0, 0, pmt(1, 256, 0, []psi.ElementaryStream{video(256), audio(257)}), 10)
	if err != nil {
		t.Fatal(err)
	}
	if iv0.StartPacket != 10 || iv0.EndPacket != -1 {
		t.Fatalf("first interval bounds wrong: %+v", iv0)
	}
	if tl.PMTVersionChanges(1) != 0 {
		t.Fatal("first PMT commit is not a change")
	}

	// Duplicate commit of the same versions must not open an interval.
	if dup, _ := tl.ApplyMapping(1, 100, 0, 0, pmt(1, 256, 0, []psi.ElementaryStream{video(256), audio(257)}), 20); dup != iv0 {
		t.Fatal("duplicate mapping must return the existing interval")
	}

	tl.ApplyPAT(pat(1, []psi.PATProgram{{ProgramNumber: 1, PMTPID: 100}}))
	iv1, err := tl.ApplyMapping(1, 100, 1, 1, pmt(1, 300, 1, []psi.ElementaryStream{video(300), audio(257)}), 40)
	if err != nil {
		t.Fatal(err)
	}
	if tl.PMTVersionChanges(1) != 1 || tl.PATVersionChanges() != 1 {
		t.Fatalf("version changes wrong: pat=%d pmt=%d", tl.PATVersionChanges(), tl.PMTVersionChanges(1))
	}
	if iv0.EndPacket != 40 || iv1.StartPacket != 40 || iv1.EndPacket != -1 {
		t.Fatalf("interval boundary not updated: iv0=%+v iv1=%+v", iv0, iv1)
	}

	intervals := tl.Intervals(1)
	if len(intervals) != 2 || intervals[0].Mapping.Streams[0].PID != 256 || intervals[1].Mapping.Streams[0].PID != 300 {
		t.Fatalf("interval mappings wrong: %+v", intervals)
	}
}

func TestTimelineAtPacketAndLookup(t *testing.T) {
	tl := NewTimeline(9)
	tl.ApplyPAT(pat(0, []psi.PATProgram{{ProgramNumber: 1, PMTPID: 100}}))
	_, _ = tl.ApplyMapping(1, 100, 0, 0, pmt(1, 256, 0, []psi.ElementaryStream{video(256), audio(257)}), 10)
	tl.ApplyPAT(pat(1, []psi.PATProgram{{ProgramNumber: 1, PMTPID: 100}}))
	_, _ = tl.ApplyMapping(1, 100, 1, 1, pmt(1, 300, 1, []psi.ElementaryStream{video(300), audio(257)}), 40)

	if _, err := tl.AtPacket(1, 9); err == nil {
		t.Fatal("packet before first mapping must error")
	}
	pre, err := tl.AtPacket(1, 39)
	if err != nil || pre.Mapping.Streams[0].PID != 256 {
		t.Fatalf("pre-switch lookup wrong: %v %v", pre, err)
	}
	post, err := tl.AtPacket(1, 40)
	if err != nil || post.Mapping.Streams[0].PID != 300 {
		t.Fatalf("post-switch lookup wrong: %v %v", post, err)
	}
	if _, err := tl.AtPacket(7, 40); err == nil {
		t.Fatal("unknown program must error")
	}

	// PID 256 only exists before the switch, PID 300 only after.
	if members := LookupPID(pre.Mapping, 256); len(members) != 1 || members[0].Stream == nil || members[0].Stream.Kind != psi.StreamVideo {
		t.Fatalf("PID 256 not attributed in pre-switch mapping: %+v", members)
	}
	if members := LookupPID(post.Mapping, 256); len(members) != 0 {
		t.Fatalf("PID 256 must be gone after remap: %+v", members)
	}
	if members := LookupPID(post.Mapping, 100); len(members) != 1 || !members[0].IsPMT {
		t.Fatalf("PMT PID role missing: %+v", members)
	}
	if members := LookupPID(post.Mapping, 300); len(members) != 1 || !members[0].IsPCR {
		t.Fatalf("PCR PID not flagged on new video PID: %+v", members)
	}
}

func TestTimelineMultiplePrograms(t *testing.T) {
	tl := NewTimeline(9)
	tl.ApplyPAT(pat(0, []psi.PATProgram{{ProgramNumber: 1, PMTPID: 100}, {ProgramNumber: 2, PMTPID: 101}}))
	if _, err := tl.ApplyMapping(1, 100, 0, 0, pmt(1, 256, 0, []psi.ElementaryStream{video(256)}), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.ApplyMapping(2, 101, 0, 0, pmt(2, 356, 0, []psi.ElementaryStream{audio(356)}), 6); err != nil {
		t.Fatal(err)
	}
	got := tl.Programs()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("program list wrong: %v", got)
	}
}
