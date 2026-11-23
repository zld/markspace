package mp4

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"mediatool/internal/timeline"
)


// TrackRun holds the parsed sample table of one traf box.
type TrackRun struct {
	TrackID         uint32
	BaseMediaDecode uint64 // tfdt, track timescale units
	TfdtPresent     bool
	TfdtVersion     uint8 // 0 for 32-bit, 1 for 64-bit; 0xff when absent
	SampleDurations []uint32
	SampleSizes     []uint32
	SampleFlags     []uint32
	DataOffset      int32  // relative to first byte of mdat payload
	HasDataOffset   bool
	ParseErrors     []*timeline.MediaError
}

// SampleCount returns the number of samples declared by trun.
func (r *TrackRun) SampleCount() int { return len(r.SampleSizes) }

// Fragment is the parsed representation of one self-contained CMAF chunk.
type Fragment struct {
	// Seq is the mfhd sequence number, or -1 when mfhd was missing.
	Seq int
	// SeqRepeated is not set by the parser; the mapping layer decides.
	Runs []*TrackRun
	// MdatPayload is the complete mdat payload.
	MdatPayload []byte
	// MdatOffset is the absolute offset of the mdat box header.
	MdatOffset uint64
	// HasMdat reports whether an mdat box was present.
	HasMdat bool
	// Errors collects non-fatal, locatable problems inside the fragment.
	Errors []*timeline.MediaError
	// Rejected is true when the fragment cannot be safely processed.
	Rejected bool
}

// SamplePayload returns the compressed bytes of one sample and hashes
// them. Callers never decode the content; the hash proves lossless remux.
func (f *Fragment) SamplePayload(runIdx, sampleIdx int) ([]byte, [32]byte, error) {
	if runIdx < 0 || runIdx >= len(f.Runs) {
		return nil, [32]byte{}, timeline.NewError(timeline.ClassFieldMissing,
			fmt.Sprintf("run index %d out of range", runIdx))
	}
	run := f.Runs[runIdx]
	if sampleIdx < 0 || sampleIdx >= len(run.SampleSizes) {
		return nil, [32]byte{}, timeline.NewError(timeline.ClassFieldMissing,
			fmt.Sprintf("sample index %d out of range", sampleIdx))
	}
	// Walk sample sizes from the start of this run's slice.
	start := 0
	for i := 0; i < sampleIdx; i++ {
		start += int(run.SampleSizes[i])
	}
	length := int(run.SampleSizes[sampleIdx])
	end := start + length
	if end > len(f.MdatPayload) {
		return nil, [32]byte{}, timeline.ErrorAt(timeline.ClassBoxTruncated,
			int64(f.MdatOffset), "sample slice exceeds mdat payload")
	}
	buf := f.MdatPayload[start:end]
	return buf, sha256.Sum256(buf), nil
}

// RunPayload returns the concatenated payload bytes owned by one run and
// the hash over all of them.
func (f *Fragment) RunPayload(runIdx int) ([]byte, [32]byte, error) {
	if runIdx < 0 || runIdx >= len(f.Runs) {
		return nil, [32]byte{}, timeline.NewError(timeline.ClassFieldMissing,
			fmt.Sprintf("run index %d out of range", runIdx))
	}
	run := f.Runs[runIdx]
	total := 0
	for _, s := range run.SampleSizes {
		total += int(s)
	}
	if total > len(f.MdatPayload) {
		return nil, [32]byte{}, timeline.ErrorAt(timeline.ClassBoxTruncated,
			int64(f.MdatOffset), "run slice exceeds mdat payload")
	}
	buf := f.MdatPayload[:total]
	return buf, sha256.Sum256(buf), nil
}

// ParseFragment parses one complete CMAF fragment from r. Malformed box
// headers are returned as fatal errors; recoverable problems inside an
// otherwise structurally valid moof are recorded on Fragment.Errors.
func ParseFragment(r io.Reader, base int64) (*Fragment, error) {
	frag := &Fragment{Seq: -1}
	var moofPayload []byte
	var moofOffset int64 = -1
	fatal := ScanBoxes(r, base, MaxFragmentSize, func(box BoxHeader, payload io.Reader) error {
		body, err := io.ReadAll(payload)
		if err != nil {
			return err
		}
		switch box.Type.String() {
		case "moof":
			moofPayload = body
			moofOffset = int64(box.PayloadOffset)
		case "mdat":
			frag.HasMdat = true
			frag.MdatOffset = box.Offset
			frag.MdatPayload = body
		}
		return nil
	})
	if fatal != nil {
		return nil, fatal
	}
	if moofPayload == nil {
		frag.Rejected = true
		frag.Errors = append(frag.Errors,
			timeline.ErrorAt(timeline.ClassFieldMissing, moofOffset, "fragment has no moof box"))
		return frag, nil
	}
	if !frag.HasMdat {
		frag.Rejected = true
		frag.Errors = append(frag.Errors,
			timeline.ErrorAt(timeline.ClassFieldMissing, moofOffset, "fragment has no mdat box"))
		return frag, nil
	}
	if err := parseMoof(frag, moofPayload, moofOffset); err != nil {
		frag.Rejected = true
		frag.Errors = append(frag.Errors, timeline.AsMediaError(err))
	}
	return frag, nil
}

// parseMoof walks the nested mfhd/traf boxes. All offsets reported in
// errors are absolute within the original stream.
func parseMoof(frag *Fragment, payload []byte, base int64) error {
	return ScanBoxes(bytes.NewReader(payload), base, MaxFragmentSize,
		func(box BoxHeader, r io.Reader) error {
			body, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			abs := int64(box.PayloadOffset)
			switch box.Type.String() {
			case "mfhd":
				seq, perr := parseMfhd(body, abs)
				if perr != nil {
					frag.Errors = append(frag.Errors, perr)
				} else {
					frag.Seq = seq
				}
			case "traf":
				run, rerr := parseTraf(body, abs)
				if rerr != nil {
					frag.Errors = append(frag.Errors, rerr)
					frag.Rejected = true
				} else {
					frag.Runs = append(frag.Runs, run)
					frag.Errors = append(frag.Errors, run.ParseErrors...)
				}
			}
			return nil
		})
}

func parseMfhd(body []byte, abs int64) (int, *timeline.MediaError) {
	// FullBox(4) + sequence_number(4)
	if len(body) < 8 {
		return -1, timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "mfhd shorter than 8 bytes")
	}
	return int(binary.BigEndian.Uint32(body[4:8])), nil
}

func parseTraf(body []byte, base int64) (*TrackRun, *timeline.MediaError) {
	run := &TrackRun{TfdtVersion: 0xff}
	var trunBodies [][]byte
	var trunOffsets []int64
	err := ScanBoxes(bytes.NewReader(body), base, MaxFragmentSize,
		func(box BoxHeader, r io.Reader) error {
			b, rerr := io.ReadAll(r)
			if rerr != nil {
				return rerr
			}
			abs := int64(box.PayloadOffset)
			switch box.Type.String() {
			case "tfhd":
				if perr := parseTfhd(b, abs, run); perr != nil {
					return perr
				}
			case "tfdt":
				if v, perr := parseTfdt(b, abs); perr != nil {
					run.ParseErrors = append(run.ParseErrors, perr)
				} else {
					run.BaseMediaDecode = v
					run.TfdtPresent = true
					run.TfdtVersion = b[0]
				}
			case "trun":
				trunBodies = append(trunBodies, b)
				trunOffsets = append(trunOffsets, abs)
			}
			return nil
		})
	if err != nil {
		return nil, timeline.AsMediaError(err)
	}
	if run.TrackID == 0 {
		return nil, timeline.ErrorAt(timeline.ClassFieldMissing, base, "traf has no tfhd track id")
	}
	for i, tb := range trunBodies {
		if perr := parseTrun(tb, trunOffsets[i], run); perr != nil {
			run.ParseErrors = append(run.ParseErrors, perr)
		}
	}
	if len(trunBodies) == 0 {
		run.ParseErrors = append(run.ParseErrors,
			timeline.ErrorAt(timeline.ClassFieldMissing, base, "traf has no trun box"))
	}
	return run, nil
}

