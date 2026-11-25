package model

import "testing"

func TestCodecSampleRate(t *testing.T) {
	rate, err := CodecOpus.SampleRate(0)
	if err != nil || rate != 48_000 {
		t.Fatalf("opus rate = %d, %v", rate, err)
	}
	if rate, err = CodecAAC.SampleRate(0); err != nil || rate != 44_100 {
		t.Fatalf("aac default rate = %d, %v", rate, err)
	}
	if rate, err = CodecAAC.SampleRate(48_000); err != nil || rate != 48_000 {
		t.Fatalf("aac explicit rate = %d, %v", rate, err)
	}
	if _, err = CodecKind("vorbis").SampleRate(0); err == nil {
		t.Fatal("unknown codec must be rejected")
	}
	if (CodecOpus).Valid() != true || (CodecKind("x")).Valid() {
		t.Fatal("codec validity wrong")
	}
}

func TestTimelineEventOrdering(t *testing.T) {
	events := []TimelineEvent{
		{Kind: EventPacket, Start: 20, End: 30, Order: 0},
		{Kind: EventPacket, Start: 10, End: 20, Order: 1},
		{Kind: EventKeyframe, Start: 10, End: 11, Order: 2},
		{Kind: EventPacket, Start: 10, End: 11, Order: 3},
	}
	out := append([]TimelineEvent(nil), events...)
	sortEventsForTest(out)
	if out[0].Start != 10 || out[0].Kind != EventKeyframe {
		t.Fatalf("kind tiebreak wrong: %+v", out)
	}
	if out[1].Order != 3 || out[2].Start != 10 || out[2].End != 20 || out[3].Start != 20 {
		t.Fatalf("order tiebreak wrong: %+v", out)
	}
}

func sortEventsForTest(events []TimelineEvent) {
	// Local insertion sort keeps the test independent of the merge package.
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].Less(events[j-1]); j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
}

func TestPacketVerdictZeroValue(t *testing.T) {
	v := PacketVerdict{}
	if v.Accepted || v.Duplicate || len(v.FindingCodes) != 0 {
		t.Fatalf("zero verdict must be empty and rejected: %+v", v)
	}
}
