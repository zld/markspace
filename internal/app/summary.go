// Package app implements the terminal-facing workflows; cmd remains a thin flag adapter.
package app

import (
	"mediacore/internal/index"
	"mediacore/internal/pipe"
)

// StableItem contains the reproducible acceptance fields for one source entry.
type StableItem struct {
	System             string `json:"system"`
	StableKey          string `json:"stable_key"`
	Manifest           string `json:"manifest"`
	Representation     string `json:"representation"`
	EntryIndex         int    `json:"entry_index"`
	OriginalSequence   int    `json:"original_sequence"`
	InputSHA256        string `json:"input_sha256"`
	IndexDigest        string `json:"index_digest"`
	UnifiedStartTick   int64  `json:"unified_start_tick"`
	UnifiedEndTick     int64  `json:"unified_end_tick"`
	SampleCount        int    `json:"sample_count"`
	FirstTickDeviation int64  `json:"first_tick_deviation"`
	LastTickDeviation  int64  `json:"last_tick_deviation"`
	FirstByteOffset    int64  `json:"first_byte_offset"`
	LastByteEnd        int64  `json:"last_byte_end"`
	FirstByteDeviation int64  `json:"first_byte_deviation"`
	LastByteDeviation  int64  `json:"last_byte_deviation"`
	Aligned            bool   `json:"aligned"`
	SourceEntryKey     string `json:"source_entry_key"`
}

type Summary struct {
	InputDigest  string                `json:"input_digest"`
	ConfigDigest string                `json:"config_digest"`
	Items        map[string]StableItem `json:"items"`
}

func BuildSummary(r *pipe.RunReport) Summary {
	s := Summary{Items: map[string]StableItem{}}
	if r == nil {
		return s
	}
	s.InputDigest = r.InputDigest
	s.ConfigDigest = r.ConfigDigest
	for _, o := range r.Outcomes {
		if o.Index == nil {
			continue
		}
		item := StableItem{
			System:           o.Entry.System,
			StableKey:        o.Entry.StableKey,
			Manifest:         o.Entry.Manifest,
			Representation:   o.Entry.Representation,
			EntryIndex:       o.Entry.EntryIndex,
			OriginalSequence: o.Entry.OriginalSequence,
			InputSHA256:      o.Index.InputSHA256,
			IndexDigest:      o.Index.Digest,
			UnifiedStartTick: o.Entry.ProgramStartU,
			UnifiedEndTick:   o.Entry.ProgramEndU,
			Aligned:          o.Entry.Aligned,
		}
		if o.Range != nil {
			item.SampleCount = o.Range.SampleCount
			item.FirstTickDeviation = o.Range.FirstTickDeviation
			item.LastTickDeviation = o.Range.LastTickDeviation
			item.FirstByteOffset = o.Range.FirstByteOffset
			item.LastByteEnd = o.Range.LastByteEnd
			item.FirstByteDeviation = o.Range.FirstByteDeviation
			item.LastByteDeviation = o.Range.LastByteDeviation
			if len(o.Range.Samples) > 0 {
				item.SourceEntryKey = o.Range.Samples[0].SourceEntryKey
			}
		} else {
			item.SampleCount = len(o.Index.Samples)
			if len(o.Index.Samples) > 0 {
				first := o.Index.Samples[0]
				last := o.Index.Samples[len(o.Index.Samples)-1]
				item.FirstByteOffset = first.ByteOffset
				item.LastByteEnd = last.ByteOffset + last.ByteSize
				item.SourceEntryKey = first.SourceEntryKey
			}
		}
		s.Items[item.StableKey] = item
	}
	return s
}

func indexDigestOrEmpty(idx *index.EntryIndex) string {
	if idx == nil {
		return ""
	}
	return idx.Digest
}
