package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"mediaflow/internal/fault"
)

// TrackExpectation states one track that an item must contain.
type TrackExpectation struct {
	ID        uint32 `json:"id"`
	Type      string `json:"type"` // "video" or "audio"
	Timescale uint32 `json:"timescale"`
}

// Item is one manifest entry and its item-specific delivery overrides.
type Item struct {
	ID             string             `json:"id"`
	Path           string             `json:"path"`
	BatchKey       string             `json:"batch_key,omitempty"`
	Width          int                `json:"width,omitempty"`
	Height         int                `json:"height,omitempty"`
	FPS            int                `json:"fps,omitempty"`
	BudgetMbps     float64            `json:"budget_mbps,omitempty"`
	QualityMin     float64            `json:"quality_min,omitempty"`
	ChunkSamples   int                `json:"chunk_samples,omitempty"`
	RequiredTracks []TrackExpectation `json:"required_tracks,omitempty"`
}

// Manifest is the offline batch input document.
type Manifest struct {
	BatchKey     string  `json:"batch_key"`
	BudgetMbps   float64 `json:"budget_mbps"`
	QualityMin   float64 `json:"quality_min"`
	ChunkSamples int     `json:"chunk_samples"`
	Items        []Item  `json:"items"`
}

// ParseManifest decodes strict JSON so typo'd parameters are not silently ignored.
func ParseManifest(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fault.Wrap(fault.ClassManifest, err, "invalid manifest JSON")
	}
	return &m, nil
}

// LoadManifest reads and parses a manifest file.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.Wrap(fault.ClassManifest, err, "read manifest")
	}
	return ParseManifest(b)
}

// Validate performs parameter and duplicate-ID checks that do not require file scanning.
// It returns item-scoped errors while retaining all entries.
func (m *Manifest) Validate() []*fault.Error {
	var errs []*fault.Error
	if m.BatchKey == "" {
		errs = append(errs, fault.New(fault.ClassManifest, "batch_key is required"))
	}
	if m.BudgetMbps <= 0 {
		errs = append(errs, fault.New(fault.ClassManifest, "budget_mbps must be positive"))
	}
	if m.QualityMin < 0 || m.QualityMin > 1 {
		errs = append(errs, fault.New(fault.ClassManifest, "quality_min must be in [0,1]"))
	}
	if m.ChunkSamples <= 0 {
		m.ChunkSamples = 64
	}
	if len(m.Items) == 0 {
		errs = append(errs, fault.New(fault.ClassManifest, "manifest contains no items"))
	}

	seen := map[string]int{}
	for i := range m.Items {
		it := &m.Items[i]
		if it.ID == "" {
			errs = append(errs, &fault.Error{Class: fault.ClassManifest, Message: fmt.Sprintf("items[%d]: id is required", i)})
			continue
		}
		seen[it.ID]++
		if it.Path == "" {
			errs = append(errs, &fault.Error{Class: fault.ClassManifest, ItemID: it.ID, Message: "path is required"})
		}
		if it.BudgetMbps < 0 || it.QualityMin < 0 || it.QualityMin > 1 {
			errs = append(errs, &fault.Error{Class: fault.ClassManifest, ItemID: it.ID, Message: "invalid item policy override"})
		}
		for _, tr := range it.RequiredTracks {
			if tr.ID == 0 {
				errs = append(errs, &fault.Error{Class: fault.ClassManifest, ItemID: it.ID, Message: "required track id must be positive"})
			}
			if tr.Type != "video" && tr.Type != "audio" {
				errs = append(errs, &fault.Error{Class: fault.ClassManifest, ItemID: it.ID, Message: fmt.Sprintf("track %d type must be video or audio", tr.ID)})
			}
			if tr.Timescale == 0 {
				errs = append(errs, &fault.Error{Class: fault.ClassTimeBase, ItemID: it.ID, Message: fmt.Sprintf("required track %d timescale must be positive", tr.ID)})
			}
		}
	}
	for id, n := range seen {
		if n > 1 {
			errs = append(errs, &fault.Error{Class: fault.ClassDuplicate, ItemID: id, Message: fmt.Sprintf("duplicate item id appears %d times", n)})
		}
	}
	return errs
}

// ItemBatchKey returns the item override or manifest batch key.
func (m *Manifest) ItemBatchKey(it Item) string {
	if it.BatchKey != "" {
		return it.BatchKey
	}
	return m.BatchKey
}
