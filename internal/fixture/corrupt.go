package fixture

import (
	"os"
	"path/filepath"

	"mediad/internal/ingest"
	"mediad/internal/model"
	"mediad/internal/pipeline"
)

// CorruptKind enumerates the deliberately corrupted fixture variants.
type CorruptKind string

const (
	// CorruptIndexOutOfRange points a key-frame entry beyond the stream end.
	CorruptIndexOutOfRange CorruptKind = "index-out-of-range"
	// CorruptTruncatedUnit cuts an access unit short.
	CorruptTruncatedUnit CorruptKind = "truncated-unit"
	// CorruptIllegalHeader flips a forbidden header bit.
	CorruptIllegalHeader CorruptKind = "illegal-header"
	// CorruptZeroTimebase carries a zero frame rate / timescale.
	CorruptZeroTimebase CorruptKind = "zero-timebase"
	// CorruptInterruption leaves an uncommitted stage directory behind.
	CorruptInterruption CorruptKind = "interruption-residue"
)

// CorruptSample is a corrupted asset plus the expected failure code.
type CorruptSample struct {
	Kind        CorruptKind
	Codec       string
	Stream      []byte
	Index       *ingest.Index
	FPS         model.Rational
	WantCode    string
	Description string
}

// Corrupt builds all four data-level corrupt variants from a valid sample.
func Corrupt(valid *Sample) ([]CorruptSample, error) {
	out := make([]CorruptSample, 0, 4)

	// 1. Out-of-range index: claim a key frame past the end of the stream.
	{
		entries := append([]ingest.IndexEntry(nil), valid.Index.Entries...)
		entries[0] = ingest.IndexEntry{
			Offset: int64(len(valid.Stream)) + 50,
			Length: 10,
			Key:    true,
		}
		// Keep the raw (invalid) entries without validation: the demo feeds
		// them to ValidateIndex, which must reject them.
		idx := &ingest.Index{Codec: valid.Codec, Entries: entries}
		out = append(out, CorruptSample{
			Kind: CorruptIndexOutOfRange, Codec: valid.Codec,
			Stream: append([]byte(nil), valid.Stream...), Index: idx, FPS: valid.FPS,
			WantCode:    model.ErrIndexOutOfRange,
			Description: "key-frame index offset beyond stream length",
		})
	}

	// 2. Truncated access unit: cut the stream short and append a final unit
	// that declares more bytes than remain, which the scanner must reject.
	{
		stream := append([]byte(nil), valid.Stream...)
		cut := len(stream) / 2
		stream = stream[:cut]
		switch valid.Codec {
		case ingest.CodecH264, ingest.CodecH265:
			// Dangling Annex-B start code with no NAL payload.
			stream = append(stream, 0, 0, 0, 1)
		case ingest.CodecAV1:
			// OBU frame header (type 3, has_size=1) declaring 20 payload
			// bytes but providing none.
			stream = append(stream, (3<<3)|0x02, 20)
		}
		// Keep the original index; its later entries now overrun the stream.
		idx := &ingest.Index{
			Codec:   valid.Codec,
			Entries: append([]ingest.IndexEntry(nil), valid.Index.Entries...),
		}
		out = append(out, CorruptSample{
			Kind: CorruptTruncatedUnit, Codec: valid.Codec,
			Stream: stream, Index: idx, FPS: valid.FPS,
			WantCode:    model.ErrTruncatedUnit,
			Description: "final access unit truncated before its end",
		})
	}

	// 3. Illegal codec header: set the forbidden bit on the first header byte.
	{
		stream := append([]byte(nil), valid.Stream...)
		switch valid.Codec {
		case ingest.CodecH264, ingest.CodecH265:
			// First NAL header sits right after the first 4-byte start code.
			stream[4] |= 0x80
		case ingest.CodecAV1:
			stream[0] |= 0x80 // OBU forbidden bit
		}
		out = append(out, CorruptSample{
			Kind: CorruptIllegalHeader, Codec: valid.Codec,
			Stream: stream, Index: valid.Index, FPS: valid.FPS,
			WantCode:    model.ErrIllegalHeader,
			Description: "forbidden header bit set",
		})
	}

	// 4. Zero time base: zero frame-rate numerator.
	{
		out = append(out, CorruptSample{
			Kind: CorruptZeroTimebase, Codec: valid.Codec,
			Stream:      append([]byte(nil), valid.Stream...),
			Index:       valid.Index,
			FPS:         model.Rational{Num: 0, Den: 1},
			WantCode:    model.ErrZeroTimebase,
			Description: "zero frame rate / timescale",
		})
	}
	return out, nil
}

// WriteInterruptionResidue simulates a crashed run: an uncommitted running
// stage directory with a partial temp artifact and a state record that never
// reached committed. It returns the work root and source digest involved.
func WriteInterruptionResidue(workRoot, sourceSHA, codec string) error {
	eng, err := pipeline.NewEngine(workRoot)
	if err != nil {
		return err
	}
	st, err := eng.LoadState(sourceSHA, codec)
	if err != nil {
		return err
	}
	// Mark an early stage committed (as if ingest finished before the crash).
	dir, err := eng.Prepare(st, pipeline.StageIngest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "ingest.out"), []byte("committed-bytes"), 0o644); err != nil {
		return err
	}
	if _, err := eng.Commit(st, pipeline.StageIngest); err != nil {
		return err
	}
	// Leave normalize half-done with a temp file and no commit.
	normDir, err := eng.Prepare(st, pipeline.StageNormalize)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(normDir, "partial.tmp"), []byte("incomplete"), 0o644); err != nil {
		return err
	}
	// Deliberately do not Commit: this directory is the interruption residue.
	return nil
}
