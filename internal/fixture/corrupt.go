package fixture

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand"

	"mediad/internal/ebml"
)

// Corruption names a deliberately damaged fixture.
type Corruption string

const (
	CorruptTruncatedElement Corruption = "truncated_element"
	CorruptInvalidVINT      Corruption = "invalid_vint"
	CorruptTimestampReverse Corruption = "timestamp_reverse"
	CorruptKeyframeMisalign Corruption = "keyframe_misalign"
	CorruptCacheTamper      Corruption = "cache_digest_tamper"
)

// CorruptCase pairs a corruption with the fixture it applies to.
type CorruptCase struct {
	Kind       Corruption     `json:"kind"`
	Data       []byte         `json:"-"`
	ExpectKind ebml.ErrorKind `json:"expect_parse_kind,omitempty"`
	// MutatedKeyframes is set by corruptions that alter timestamps instead of
	// bytes, so the alignment stage (not the parser) must reject the fixture.
	MutatedKeyframes []int64 `json:"-"`
	// Explain is a one-line human description for terminal output.
	Explain string `json:"explain"`
}

// Corrupt produces one damaged fixture per named corruption, derived from a
// clean base document. Every case is deterministic for a given seed.
func Corrupt(base *Result, seed int64) ([]CorruptCase, error) {
	if base == nil || len(base.Data) == 0 {
		return nil, errors.New("fixture: empty base document")
	}
	rng := rand.New(rand.NewSource(seed))
	cases := []CorruptCase{
		truncateElement(base, rng),
		breakVINT(base),
		reverseTimestamp(base, rng),
		misalignKeyframes(base),
		{
			Kind:    CorruptCacheTamper,
			Explain: "cache metadata rewritten to claim a different content digest",
		},
	}
	return cases, nil
}

// truncateElement cuts the document inside a SimpleBlock payload so the block's
// declared size overruns EOF.
func truncateElement(base *Result, rng *rand.Rand) CorruptCase {
	data := append([]byte{}, base.Data...)
	// Find a SimpleBlock ID and cut a few bytes into its payload.
	idx := bytes.Index(data, []byte{0xA3})
	if idx < 0 || idx+40 >= len(data) {
		return CorruptCase{Kind: CorruptTruncatedElement, Data: data, ExpectKind: ebml.ErrKindTruncated, Explain: "no block to truncate"}
	}
	cut := idx + 20 + rng.Intn(10)
	return CorruptCase{
		Kind:       CorruptTruncatedElement,
		Data:       data[:cut],
		ExpectKind: ebml.ErrKindTruncated,
		// Truncation may surface as a truncated block or a segment that ends
		// early; both are categorized as truncation, never silent success.
		Explain: "document truncated inside a SimpleBlock payload",
	}
}

// breakVINT writes a zero first byte where an element ID is expected, which is
// not a legal EBML variable-length ID.
func breakVINT(base *Result) CorruptCase {
	data := append([]byte{}, base.Data...)
	// Overwrite the first byte of the last Cluster's timestamp payload region.
	idx := bytes.LastIndex(data, []byte{0x1F, 0x43, 0xB6, 0x75})
	if idx < 0 {
		return CorruptCase{Kind: CorruptInvalidVINT, Data: data, ExpectKind: ebml.ErrKindInvalidVINT, Explain: "no cluster"}
	}
	// Point just past cluster header+size at the timestamp ID and zero it.
	pos := idx
	for pos < len(data) && data[pos] != 0xE7 {
		pos++
	}
	if pos >= len(data) {
		pos = idx + 4 + 2
	}
	if pos < len(data) {
		data[pos] = 0x00
	}
	return CorruptCase{
		Kind:       CorruptInvalidVINT,
		Data:       data,
		ExpectKind: ebml.ErrKindInvalidVINT,
		Explain:    "zero-byte where an element ID is required",
	}
}

// reverseTimestamp swaps two block timecodes so one track moves backwards.
func reverseTimestamp(base *Result, rng *rand.Rand) CorruptCase {
	data := append([]byte{}, base.Data...)
	if len(data) < 200 {
		return CorruptCase{Kind: CorruptTimestampReverse, Data: data, Explain: "document too small"}
	}
	// Find a SimpleBlock in a later cluster. Layout is A3 | size | track VINT |
	// int16 timecode | flags. Track 1 uses a one-byte VINT, so the timecode
	// sits at idx+3..idx+4.
	searchFrom := 0
	idx := -1
	for i := 0; i < 4; i++ {
		next := bytes.Index(data[searchFrom:], []byte{0xA3})
		if next < 0 {
			break
		}
		idx = searchFrom + next
		searchFrom = idx + 1
	}
	if idx < 0 || idx+5 > len(data) {
		return CorruptCase{Kind: CorruptTimestampReverse, Data: data, Explain: "no mutable block"}
	}
	// Rewrite the two-byte signed timecode to -1 so this block moves backwards
	// relative to its predecessor.
	binary.BigEndian.PutUint16(data[idx+3:idx+5], 0xFFFF)
	return CorruptCase{
		Kind:             CorruptTimestampReverse,
		Data:             data,
		MutatedKeyframes: nil,
		// Timecode reversal is a timeline failure, not a parse failure; the
		// clock stage must reject it with an Issue.
		Explain: "one block's relative timecode set to -1",
	}
}

// misalignKeyframes shifts one representation's keyframe grid by more than one
// frame, which the aligner must reject rather than silently cutting there.
func misalignKeyframes(base *Result) CorruptCase {
	if len(base.VideoKeyframes) < 2 {
		return CorruptCase{Kind: CorruptKeyframeMisalign, Explain: "no keyframes"}
	}
	mutated := append([]int64(nil), base.VideoKeyframes...)
	mutated[1] += 250_000_000
	return CorruptCase{
		Kind:             CorruptKeyframeMisalign,
		MutatedKeyframes: mutated,
		Explain:          "second representation keyframe shifted by 250ms",
	}
}

// WriteCorruptFixtures returns the raw bytes of the document-based corruptions
// so the CLI can persist them for the demo.
func WriteCorruptFixtures(base *Result) ([][]byte, error) {
	cases, err := Corrupt(base, 1)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, c := range cases {
		if c.Data != nil {
			out = append(out, c.Data)
		}
	}
	return out, nil
}
