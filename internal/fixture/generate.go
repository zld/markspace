// Package fixture: generate.go owns the event-JSON and task-parameter schemas
// and writes a browsable sample input tree for the `generate` command. The
// selftest uses the same in-process builders, so the files on disk and the
// verified bytes can never drift.
package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"

	"replaykit/media/audio"
)

// EventJSON is one event as represented in events.json (nanoseconds).
type EventJSON struct {
	ID     string `json:"id"`
	AtNS   int64  `json:"at_ns"`
	PreNS  int64  `json:"pre_ns,omitempty"`
	PostNS int64  `json:"post_ns,omitempty"`
}

// EventFile is the top-level events.json document.
type EventFile struct {
	DefaultPreNS  int64       `json:"default_pre_ns"`
	DefaultPostNS int64       `json:"default_post_ns"`
	Events        []EventJSON `json:"events"`
}

// LayerParam describes one bitrate rendition input.
type LayerParam struct {
	Name       string `json:"name"`
	File       string `json:"file"`
	Timescale  uint32 `json:"timescale"`
	FrameTicks int64  `json:"frame_ticks"`
}

// TaskParams is the task parameter document (codec, rate, AAC AU config,
// layers and byte limits).
type TaskParams struct {
	Audio           audio.Params `json:"audio"`
	Layers          []LayerParam `json:"layers"`
	ReorderWindowMS int          `json:"reorder_window_ms"`
	MaxBoxBytes     int64        `json:"max_box_bytes"`
	MaxSegmentBytes int64        `json:"max_segment_bytes"`
}

// ProofEventFile returns the canonical three-event document with expansion.
func ProofEventFile() EventFile {
	evs := make([]EventJSON, 0, len(ProofEventsNS))
	for i, at := range ProofEventsNS {
		evs = append(evs, EventJSON{
			ID:     idFor(i),
			AtNS:   at,
			PreNS:  PreNS,
			PostNS: PostNS,
		})
	}
	return EventFile{DefaultPreNS: PreNS, DefaultPostNS: PostNS, Events: evs}
}

func idFor(i int) string {
	return []string{"goal", "chance", "save"}[i%3] + "-" + string(rune('1'+i))
}

// ProofTaskParams returns parameters matching the generated proof samples.
func ProofTaskParams() TaskParams {
	return TaskParams{
		Audio:           OpusClean(1).Params,
		ReorderWindowMS: 80,
		MaxBoxBytes:     8 << 20,
		MaxSegmentBytes: 16 << 20,
		Layers: []LayerParam{
			{Name: "720p", File: "video/720p.fmp4", Timescale: HiTimescale, FrameTicks: int64(HiFrameTicks)},
			{Name: "360p", File: "video/360p.fmp4", Timescale: LoTimescale, FrameTicks: int64(LoFrameTicks)},
		},
	}
}

// WriteJSON writes v as indented JSON, creating parent directories.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

// LoadEvents reads an events.json document.
func LoadEvents(path string) (EventFile, error) {
	var ef EventFile
	b, err := os.ReadFile(path)
	if err != nil {
		return ef, err
	}
	if err := json.Unmarshal(b, &ef); err != nil {
		return ef, err
	}
	if ef.DefaultPreNS == 0 {
		ef.DefaultPreNS = PreNS
	}
	if ef.DefaultPostNS == 0 {
		ef.DefaultPostNS = PostNS
	}
	return ef, nil
}

// WriteSampleTree materialises the normal sample inputs under dir and returns
// the paths written. It is used by the `generate` command.
func WriteSampleTree(dir string) ([]string, error) {
	var written []string
	must := func(rel string, b []byte) error {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		written = append(written, rel)
		return nil
	}
	if err := WriteJSON(filepath.Join(dir, "events.json"), ProofEventFile()); err != nil {
		return nil, err
	}
	written = append(written, "events.json")
	if err := WriteJSON(filepath.Join(dir, "params.json"), ProofTaskParams()); err != nil {
		return nil, err
	}
	written = append(written, "params.json")
	if err := must("video/720p.fmp4", NormalHigh()); err != nil {
		return nil, err
	}
	if err := must("video/360p.fmp4", NormalLow()); err != nil {
		return nil, err
	}
	if err := must("audio/opus.rtp", OpusClean(40).Framed); err != nil {
		return nil, err
	}
	if err := must("audio/aac.rtp", AACClean(30).Framed); err != nil {
		return nil, err
	}
	if err := must("audio/loss-dup.rtp", LossDup().Framed); err != nil {
		return nil, err
	}
	return written, nil
}
