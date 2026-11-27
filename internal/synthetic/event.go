package synthetic

import (
	"bytes"
	"encoding/json"
	"math/rand"
)

// Event is one client-side playback event. RawNS is the drifting client clock;
// ReferenceNS is the ground-truth media position the generator aimed at.
type Event struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RawNS       int64  `json:"raw_ns"`
	ReferenceNS int64  `json:"reference_ns"`
	// Late marks events whose arrival is intentionally delayed past watermark.
	Late        bool   `json:"late,omitempty"`
}

// EventConfig controls the generated event stream.
type EventConfig struct {
	Count       int
	RatePPM     float64 // client clock rate error
	OffsetNS    int64   // fixed client clock offset
	LateIndices []int   // events emitted after the watermark passes
}

// BuildEvents generates Count events at fixed 1s reference spacing starting at
// t=500ms, applying offset + rate drift to obtain raw client timestamps.
func BuildEvents(rng *rand.Rand, cfg EventConfig) []Event {
	late := map[int]struct{}{}
	for _, i := range cfg.LateIndices {
		late[i] = struct{}{}
	}
	evs := make([]Event, cfg.Count)
	for i := 0; i < cfg.Count; i++ {
		ref := int64(500_000_000) + int64(i)*1_000_000_000
		// Inverse of DriftModel.Apply: raw = origin + (ref-origin-offset)/(1+rate)
		corr := float64(ref-cfg.OffsetNS) / (1 + cfg.RatePPM/1e6)
		raw := int64(corr + 0.5)
		_, isLate := late[i]
		evs[i] = Event{
			ID: eventID(i), Name: eventName(i),
			RawNS: raw, ReferenceNS: ref, Late: isLate,
		}
	}
	return evs
}

func eventID(i int) string {
	const digits = "0123456789abcdef"
	if i < 16 {
		return "ev-00" + string(digits[i])
	}
	return "ev-" + string(digits[i>>4]) + string(digits[i&0xf])
}

func eventName(i int) string {
	names := []string{"play", "stall", "resume", "bitrate-switch", "frame-drop"}
	return names[i%len(names)]
}

// EncodeEvents writes events as newline-delimited JSON.
func EncodeEvents(evs []Event) ([]byte, error) {
	var buf []byte
	enc := json.NewEncoder(jsonWriter{buf: &buf})
	for i := range evs {
		if err := enc.Encode(&evs[i]); err != nil {
			return nil, err
		}
	}
	return buf, nil
}

type jsonWriter struct{ buf *[]byte }

func (w jsonWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

// DecodeEvents reads newline-delimited JSON events.
func DecodeEvents(data []byte) ([]Event, error) {
	var evs []Event
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var e Event
		if err := dec.Decode(&e); err != nil {
			return nil, err
		}
		evs = append(evs, e)
	}
	return evs, nil
}
