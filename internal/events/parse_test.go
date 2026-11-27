package events

import (
	"strings"
	"testing"

	"rtp-rebuild/internal/clock"
)

const sampleLog = `# client events
1700000000000 stall id=3 text=buffering
1700000000500 resume id=3
2023-11-14T22:13:20.750Z seek position=120
+2500ms freeze id=9
+90000t join id=4
+1.5s resume id=9
bogus-time stall id=99
+oops stall id=100
`

func TestParseRelativeAndWall(t *testing.T) {
	log := Parse([]byte(sampleLog))
	if len(log.Events) != 8 {
		t.Fatalf("events = %d, want 8", len(log.Events))
	}
	first := log.Events[0]
	if first.Kind != KindStall || first.WallMillis != 1700000000000 {
		t.Fatalf("first = %+v", first)
	}
	if first.Attrs["id"] != "3" || first.Attrs["text"] != "buffering" {
		t.Fatalf("attrs = %v", first.Attrs)
	}
	if log.Events[1].Kind != KindResume {
		t.Fatalf("second kind = %s", log.Events[1].Kind)
	}
	// RFC3339 timestamps are converted to Unix milliseconds.
	if log.Events[2].WallMillis != 1700000000750 {
		t.Fatalf("rfc3339 wall = %d", log.Events[2].WallMillis)
	}
	rel := log.Events[3]
	if !rel.HasRelative || rel.Relative != clock.Tick(2500*90) {
		t.Fatalf("relative = %+v", rel)
	}
	if log.Events[4].Relative != clock.Tick(90000) {
		t.Fatalf("tick relative = %d", log.Events[4].Relative)
	}
	if log.Events[5].Relative != clock.Tick(1.5*90000) {
		t.Fatalf("second relative = %d", log.Events[5].Relative)
	}
}

func TestUnalignableEventKept(t *testing.T) {
	log := Parse([]byte("bogus-time stall id=99\n+oops stall id=100\n1700000000000 stall id=1\n"))
	if len(log.Events) != 3 {
		t.Fatalf("events = %d, want 3 (bad events are kept)", len(log.Events))
	}
	if log.Unalignable != 2 {
		t.Fatalf("unalignable = %d, want 2", log.Unalignable)
	}
	if log.Events[0].Reason == "" || log.Events[1].Reason == "" {
		t.Fatal("bad events must carry a reason")
	}
	if log.Events[2].Reason != "" {
		t.Fatalf("valid event has reason %q", log.Events[2].Reason)
	}
}

func TestParseErrorsDoNotStopParsing(t *testing.T) {
	log := Parse([]byte("+99999999999999999999t stall\n+100ms stall ok=1\n"))
	if len(log.Events) != 2 {
		t.Fatalf("events = %d", len(log.Events))
	}
	if log.Events[0].Reason == "" {
		t.Fatal("overflowing relative time should be unalignable")
	}
	if log.Events[1].Reason != "" {
		t.Fatalf("valid event rejected: %q", log.Events[1].Reason)
	}
}

func TestIsAlignable(t *testing.T) {
	wall := Event{WallMillis: 1700000000500}
	if !wall.IsAlignable(1700000000000, true) {
		t.Fatal("wall event should align with a capture start")
	}
	if wall.IsAlignable(0, false) {
		t.Fatal("wall event must not align without a capture start")
	}
	rel := Event{HasRelative: true, Relative: 900}
	if !rel.IsAlignable(0, false) {
		t.Fatal("relative event should always align")
	}
	empty := Event{}
	if empty.IsAlignable(1700000000000, true) {
		t.Fatal("event without a timestamp must not align")
	}
}

func TestMediaFromWall(t *testing.T) {
	tick, err := MediaFromWall(1700000000500, 1700000000000)
	if err != nil {
		t.Fatalf("MediaFromWall: %v", err)
	}
	if tick != clock.Tick(500*90) {
		t.Fatalf("tick = %d, want %d", tick, 500*90)
	}
	if _, err := MediaFromWall(-1<<62, 1<<62); err == nil {
		t.Fatal("expected overflow")
	}
}

func TestEmptyAndCommentLines(t *testing.T) {
	log := Parse([]byte("\n# comment\n   \n"))
	if len(log.Events) != 0 {
		t.Fatalf("events = %d, want 0", len(log.Events))
	}
}

func TestUnknownKindKept(t *testing.T) {
	log := Parse([]byte("+10ms rebuffer id=1\n"))
	if len(log.Events) != 1 || string(log.Events[0].Kind) != "rebuffer" {
		t.Fatalf("events = %+v", log.Events)
	}
}

func TestTextWithSpaces(t *testing.T) {
	log := Parse([]byte("+10ms stall id=1 text=the player stopped\n"))
	if got := log.Events[0].Attrs["text"]; got != "the player stopped" {
		t.Fatalf("text = %q", got)
	}
	if !strings.Contains(log.Events[0].Raw, "player stopped") {
		t.Fatalf("raw = %q", log.Events[0].Raw)
	}
}
