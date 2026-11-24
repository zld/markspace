package ingest

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"mediarc/internal/model"
)

// InjectedLocation records where the generator deliberately damaged input.
type InjectedLocation struct {
	Kind         model.Kind `json:"kind"`
	LogicalIndex int        `json:"logical_index"`
	Offset       int64      `json:"offset"`
}

// FrameGenOptions controls deterministic frame fixture generation.
type FrameGenOptions struct {
	Path         string
	MaterialID   string
	Seed         int64
	Frames       int
	Width        uint16
	Height       uint16
	Duration     uint32
	BadSHA       bool
	Truncated    bool
	Duplicate    bool
	OutOfOrder   bool
}

// FrameFixture describes generated frame records and known damage offsets.
type FrameFixture struct {
	Path           string              `json:"path"`
	MaterialID     string              `json:"material_id"`
	LogicalFrames  int                 `json:"logical_frames"`
	RecordCount    int                 `json:"record_count"`
	BadSHA         *InjectedLocation   `json:"bad_sha,omitempty"`
	Truncated      *InjectedLocation   `json:"truncated,omitempty"`
	DuplicateIndex int                 `json:"duplicate_index,omitempty"`
	SwappedPairs   [][2]int            `json:"swapped_pairs,omitempty"`
}

// GenerateFrameFile creates the self-contained MRC1 frame fixture.
func GenerateFrameFile(o FrameGenOptions) (*FrameFixture, error) {
	if o.Frames <= 0 {
		return nil, fmt.Errorf("frames must be positive")
	}
	if o.MaterialID == "" {
		o.MaterialID = "MAT-16025"
	}
	if o.Width == 0 {
		o.Width = 320
	}
	if o.Height == 0 {
		o.Height = 180
	}
	if o.Duration == 0 {
		o.Duration = uint32(model.VideoFrameDelta)
	}
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o755); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewSource(o.Seed))
	badIndex := -1
	truncIndex := -1
	if o.BadSHA {
		badIndex = clampIndex(o.Frames, o.Frames/3+rng.Intn(max1(o.Frames/3)))
	}
	if o.Truncated {
		truncIndex = clampIndex(o.Frames, 2*o.Frames/3+rng.Intn(max1(o.Frames/4)))
		if truncIndex == badIndex {
			truncIndex = (truncIndex + 1) % o.Frames
		}
	}

	records := make([]preparedFrame, o.Frames)
	color := model.ColorMetadata{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: "limited"}
	for i := 0; i < o.Frames; i++ {
		payloadLen := 128 + (i%13)*8
		payload := make([]byte, payloadLen)
		_, _ = rng.Read(payload)
		// A deterministic non-random marker makes fixtures inspectable.
		for j := 0; j < len(payload); j += 16 {
			payload[j] = byte(i)
		}
		rec := encodeFrame(o.MaterialID, uint32(i), o.Duration, o.Width, o.Height, color, payload, i%8 == 0)
		pf := preparedFrame{logicalIndex: i, record: rec}
		if i == badIndex {
			pf.badSHA = true
			rec[8+FrameHeaderSize] ^= 0xff
		}
		if i == truncIndex {
			pf.truncated = true
			cut := 9 + i%7
			rec = rec[:len(rec)-cut]
			binary.BigEndian.PutUint32(rec[4:8], uint32(len(rec)-8))
			pf.record = rec
		}
		records[i] = pf
	}

	order := make([]int, o.Frames)
	for i := range order {
		order[i] = i
	}
	fx := &FrameFixture{Path: o.Path, MaterialID: o.MaterialID, LogicalFrames: o.Frames, DuplicateIndex: -1}
	if o.OutOfOrder && o.Frames >= 4 {
		positions := []int{o.Frames / 4, o.Frames / 2}
		for _, p := range positions {
			if p+1 < o.Frames {
				order[p], order[p+1] = order[p+1], order[p]
				fx.SwappedPairs = append(fx.SwappedPairs, [2]int{p, p + 1})
			}
		}
	}

	f, err := os.Create(o.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var offset int64
	writeAt := func(rec []byte) error {
		n, err := f.Write(rec)
		offset += int64(n)
		return err
	}
	for _, idx := range order {
		pf := records[idx]
		start := offset
		if err := writeAt(pf.record); err != nil {
			return nil, err
		}
		if pf.badSHA {
			fx.BadSHA = &InjectedLocation{Kind: model.FaultBadSHA, LogicalIndex: idx,
				Offset: start + 8 + FrameHeaderSize}
		}
		if pf.truncated {
			envLen := binary.BigEndian.Uint32(pf.record[4:8])
			actualPayload := int64(envLen) - FrameHeaderSize
			fx.Truncated = &InjectedLocation{Kind: model.FaultTruncated, LogicalIndex: idx,
				Offset: start + 8 + FrameHeaderSize + actualPayload}
		}
	}
	fx.RecordCount = o.Frames
	if o.Duplicate && o.Frames > 2 {
		idx := 5 % o.Frames
		if err := writeAt(records[idx].record); err != nil {
			return nil, err
		}
		fx.RecordCount++
		fx.DuplicateIndex = idx
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	return fx, nil
}

func clampIndex(n, i int) int {
	if n <= 1 {
		return 0
	}
	if i < 0 || i >= n {
		return i % n
	}
	return i
}

func max1(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}
