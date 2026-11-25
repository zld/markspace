package stream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"assetcheck/internal/mediaclock"
)

// Summary is the deterministic, operational-field-free identity of an
// analysis plus its verdict. It is what repeated runs must reproduce.
type Summary struct {
	ContentFingerprint string              `json:"content-fingerprint"`
	SampleRate         int                 `json:"sample-rate"`
	Channels           int                 `json:"channels"`
	TotalFrames        int64               `json:"total-frames"`
	PresentFrames      int64               `json:"present-frames"`
	MaxIntervalFrames  int64               `json:"max-interval-frames"`
	CorruptRanges      []mediaclock.Range  `json:"corrupt-ranges"`
	CorruptFrames      int64               `json:"corrupt-frames"`
	CorruptRatio       float64             `json:"corrupt-ratio"`
	Items              []Item              `json:"items"`
}

// DigestSummary returns the canonical "sha256:..." summary digest.
func DigestSummary(contentFP string, rep *Report, ev *Evaluation) string {
	s := Summary{
		ContentFingerprint: contentFP,
		SampleRate:         rep.SampleRate,
		Channels:           rep.Channels,
		TotalFrames:        rep.TotalFrames,
		PresentFrames:      rep.PresentFrames,
		MaxIntervalFrames:  rep.Interval.MaxFrames,
		CorruptRanges:      rep.CorruptRanges,
		CorruptFrames:      rep.CorruptFrames,
		CorruptRatio:       rep.CorruptRatio,
	}
	if ev != nil {
		s.Items = ev.Items
	}
	b, err := json.Marshal(s)
	if err != nil {
		// All fields are JSON-safe; a panic here would be a programming bug.
		panic(err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
