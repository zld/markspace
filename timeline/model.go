// Package timeline converts client events, RTP packets and MP4/CMAF segment
// timestamps onto one int64-nanosecond time base, advances a watermark over
// event time and replays late arrivals from checkpoints.
package timeline

// Kind labels the source timeline an item came from.
const (
	KindClientEvent = "event"
	KindRTP         = "rtp"
	KindMP4         = "mp4"
)

// Item is a single point on the unified timeline. NS is the corrected
// media-timeline position in nanoseconds; all other timestamps stay raw.
type Item struct {
	Kind      string `json:"kind"`
	Source    string `json:"source"`              // track id, ssrc, event stream
	NS        int64  `json:"ns"`                  // unified, corrected position
	RawNS     int64  `json:"raw_ns"`              // position before drift correction
	EndNS     int64  `json:"end_ns,omitempty"`    // frame/sample end on unified base
	Sequence  uint64 `json:"sequence,omitempty"`  // rtp seq (unwrapped) or sample index
	Timestamp uint64 `json:"timestamp,omitempty"` // rtp ts (unwrapped) or tfdt base
	Key       string `json:"key,omitempty"`       // event name / payload key
	Digest    []byte `json:"digest,omitempty"`    // sha256 of payload for idempotency
}

// Gap is a closed window whose missing sequence numbers never arrived.
type Gap struct {
	Source       string `json:"source"`
	FromSequence uint64 `json:"from_sequence"`
	ToSequence   uint64 `json:"to_sequence"`
	WindowClosed int64  `json:"window_closed_ns"`
}

// Summary is the deterministic digest of a committed timeline prefix.
type Summary struct {
	WatermarkNS   int64    `json:"watermark_ns"`
	ItemCount     int      `json:"item_count"`
	GapCount      int      `json:"gap_count"`
	ReplayCount   int      `json:"replay_count"`
	Digest        string   `json:"digest_hex"`
	Gaps          []Gap    `json:"gaps,omitempty"`
}

// Identity is the stable dedup key of an item. Two arrivals with the same
// identity are the same datum regardless of arrival time or ordering.
func (it Item) Identity() string {
	return it.Kind + "|" + it.Source + "|" + it.Key + "|" +
		uintToText(it.Sequence) + "|" + string(it.Digest)
}

func uintToText(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for v > 0 {
		n--
		b[n] = byte('0' + v%10)
		v /= 10
	}
	return string(b[n:])
}
