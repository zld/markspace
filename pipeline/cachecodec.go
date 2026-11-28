package pipeline

import (
	"encoding/json"
	"fmt"

	"mediad/index"
	"mediad/timeline"
)

// cachedEvent is the JSON-stable form of a timeline.Event. Rational values and
// timestamps are stored as their exact numerator/denominator and base so a
// cached event is bit-identical to a freshly parsed one.
type cachedEvent struct {
	Kind         string          `json:"kind"`
	Scheme       string          `json:"scheme,omitempty"`
	Time         cachedTimestamp `json:"time"`
	Presentation cachedRational  `json:"presentation"`
	Duration     cachedRational  `json:"duration"`
	Payload      []byte          `json:"payload"`
	Source       timeline.Source `json:"source"`
	Recovered    bool            `json:"recovered,omitempty"`
}

type cachedRational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

type cachedTimestamp struct {
	Ticks     int64  `json:"ticks"`
	TimeScale uint32 `json:"timescale"`
	WrapAt    int64  `json:"wrap_at,omitempty"`
	Name      string `json:"name,omitempty"`
}

type cachedIssues struct {
	Issues timeline.Issues `json:"issues"`
}

func encodeRational(r timeline.Rational) cachedRational {
	return cachedRational{Num: r.Num(), Den: r.Den()}
}

func decodeRational(r cachedRational) (timeline.Rational, error) {
	return timeline.NewRational(r.Num, r.Den)
}

// storeCachedEvents serialises parsed events and issues into a cache entry
// keyed by the metadata fingerprint.
func storeCachedEvents(c *index.Cache, path, metadataSum string, events []timeline.Event, issues timeline.Issues) error {
	encoded := make([]cachedEvent, 0, len(events))
	for _, e := range events {
		encoded = append(encoded, cachedEvent{
			Kind:   string(e.Kind),
			Scheme: e.Scheme,
			Time: cachedTimestamp{
				Ticks: e.Time.Ticks, TimeScale: e.Time.Base.TimeScale, WrapAt: e.Time.Base.WrapAt, Name: e.Time.Base.Name,
			},
			Presentation: encodeRational(e.Presentation),
			Duration:     encodeRational(e.Duration),
			Payload:      e.Payload,
			Source:       e.Source,
			Recovered:    e.Recovered,
		})
	}
	evJSON, err := json.Marshal(encoded)
	if err != nil {
		return err
	}
	isJSON, err := json.Marshal(cachedIssues{Issues: issues})
	if err != nil {
		return err
	}
	return c.Put(&index.CacheEntry{Path: path, Metadata: metadataSum, Version: 0, EventsJSON: evJSON, IssuesJSON: isJSON})
}

// decodeCachedEvents reconstructs events and issues from a cache entry. A decode
// error (from an older cache layout) becomes a cache miss at the caller.
func decodeCachedEvents(e *index.CacheEntry) ([]timeline.Event, timeline.Issues, error) {
	var encoded []cachedEvent
	if err := json.Unmarshal(e.EventsJSON, &encoded); err != nil {
		return nil, nil, err
	}
	events := make([]timeline.Event, 0, len(encoded))
	for i, ce := range encoded {
		pres, err := decodeRational(ce.Presentation)
		if err != nil {
			return nil, nil, fmt.Errorf("event %d presentation: %w", i, err)
		}
		dur, err := decodeRational(ce.Duration)
		if err != nil {
			return nil, nil, fmt.Errorf("event %d duration: %w", i, err)
		}
		events = append(events, timeline.Event{
			Kind:         timeline.Kind(ce.Kind),
			Scheme:       ce.Scheme,
			Time:         timeline.Timestamp{Ticks: ce.Time.Ticks, Base: timeline.Base{Name: ce.Time.Name, TimeScale: ce.Time.TimeScale, WrapAt: ce.Time.WrapAt}},
			Presentation: pres,
			Duration:     dur,
			Payload:      ce.Payload,
			Source:       ce.Source,
			Recovered:    ce.Recovered,
		})
	}
	var is cachedIssues
	if len(e.IssuesJSON) > 0 {
		if err := json.Unmarshal(e.IssuesJSON, &is); err != nil {
			return nil, nil, err
		}
	}
	return events, is.Issues, nil
}
