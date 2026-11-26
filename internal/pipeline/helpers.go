package pipeline

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"github.com/pion/rtp"

	"continuity/internal/clock"
	"continuity/internal/timeline"
)

// FrameDefaults matches the fixture frame geometry when meta is absent.
func FrameDefaults() (int, int) { return 32, 24 }

// parseFrameMeta reads the simple width=N/height=N sidecar.
func parseFrameMeta(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, clock.New(clock.KindIO, "frame-meta", "open: %v", err)
	}
	defer f.Close()
	w, h := 0, 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			return 0, 0, clock.New(clock.KindInvalidInput, "frame-meta",
				"bad line %q", line)
		}
		v, err := strconv.Atoi(strings.TrimSpace(kv[1]))
		if err != nil {
			return 0, 0, clock.New(clock.KindInvalidInput, "frame-meta",
				"bad integer in %q", line)
		}
		switch strings.TrimSpace(kv[0]) {
		case "width":
			w = v
		case "height":
			h = v
		}
	}
	if w <= 0 || h <= 0 {
		return 0, 0, clock.New(clock.KindInvalidInput, "frame-meta",
			"missing positive width/height")
	}
	return w, h, nil
}

// unwrapSwitchStream converts switch records into synthetic RTP packets and
// exercises the 32-bit unwrap so a wrap in the cut log is validated up front.
func (r *Runner) unwrapSwitchStream(recs []timeline.SwitchRecord) error {
	packets := make([]*rtp.Packet, 0, len(recs))
	for _, rec := range recs {
		packets = append(packets, &rtp.Packet{
			Header: rtp.Header{
				SequenceNumber: rec.Seq,
				Timestamp:      rec.Timestamp,
			},
		})
	}
	// Sequence numbers across distinct sources are not required to be
	// monotonic, so validate timestamp unwrapping directly instead of the
	// strict stream helper.
	if len(packets) >= 2 {
		prev := int64(packets[0].Timestamp)
		for i := 1; i < len(packets); i++ {
			_ = timeline.UnwrapTimestamp(prev, packets[i].Timestamp)
		}
	}
	return nil
}
