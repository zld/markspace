package fixture

import (
	"fmt"
	"strings"
)

// ClientEvent is one synthetic client log event.
type ClientEvent struct {
	TimeMS int64
	Kind   string
	Detail string
}

// ClientLog renders a newline-separated client event log. Lines have the form
//
//	<time_ms>\t<kind>\t<detail>
//
// The time base is milliseconds, converted to nanoseconds by the clock layer.
func ClientLog(events []ClientEvent) []byte {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "%d\t%s\t%s\n", e.TimeMS, e.Kind, e.Detail)
	}
	return []byte(b.String())
}

// DefaultEvents returns the event list the demo maps onto the healthy sample.
// Times fall inside the media span (0..~800 ms) so interpolation is exercised.
func DefaultEvents() []ClientEvent {
	return []ClientEvent{
		{TimeMS: 0, Kind: "join", Detail: "room-a"},
		{TimeMS: 120, Kind: "freeze", Detail: "video-stall"},
		{TimeMS: 260, Kind: "recover", Detail: "video-resume"},
		{TimeMS: 410, Kind: "freeze", Detail: "audio-glitch"},
		{TimeMS: 640, Kind: "recover", Detail: "ok"},
	}
}

// ParseClientLog parses the log format emitted by ClientLog. Blank lines are
// skipped; malformed lines return an error naming the line number.
func ParseClientLog(data []byte) ([]ClientEvent, error) {
	var out []ClientEvent
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			return nil, fmt.Errorf("line %d: malformed event %q", i+1, line)
		}
		var ms int64
		if _, err := fmt.Sscanf(parts[0], "%d", &ms); err != nil {
			return nil, fmt.Errorf("line %d: time %q is not an integer", i+1, parts[0])
		}
		ev := ClientEvent{TimeMS: ms, Kind: parts[1]}
		if len(parts) == 3 {
			ev.Detail = parts[2]
		}
		out = append(out, ev)
	}
	return out, nil
}
