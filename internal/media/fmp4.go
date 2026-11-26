package media

import (
	"fmt"
	"math"
	"os"

	"github.com/abema/go-mp4"
)

// Sample is one normalized fMP4 sample timestamp/range.
type Sample struct {
	Index      int
	PTSNS      int64
	DurationNS int64
	Size       uint32
	DataOffset int64
}

// Fragment contains the boxes and sample timestamps needed by composition.
type Fragment struct {
	Path       string
	Sequence   uint32
	TrackID    uint32
	Timescale  uint32
	BaseNS     int64
	Samples    []Sample
	PayloadEnd int64
	FileSize   int64
}

// ReadFragment reads moof/tfdt/trun boxes with go-mp4 and verifies that all
// declared sample bytes are actually present in mdat.
func ReadFragment(path string, timescale uint32) (*Fragment, error) {
	if timescale == 0 {
		timescale = 1_000_000_000
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, &Fault{Phase: PhaseMP4, Code: CodeInvalidMP4, Source: path, Err: err}
	}
	if st.Size() > MaxInputBytes {
		return nil, &Fault{Phase: PhaseMP4, Code: CodeSizeLimit, Source: path, Offset: st.Size()}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, &Fault{Phase: PhaseMP4, Code: CodeInvalidMP4, Source: path, Err: err}
	}
	defer f.Close()

	out := &Fragment{Path: path, Timescale: timescale, FileSize: st.Size()}
	if boxes, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{mp4.BoxTypeMoof(), mp4.BoxTypeMfhd()}); err != nil {
		return nil, mp4Fault(path, "mfhd", err)
	} else if len(boxes) > 0 {
		if mfhd, ok := boxes[0].Payload.(*mp4.Mfhd); ok {
			out.Sequence = mfhd.SequenceNumber
		}
	}
	if boxes, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTfhd()}); err != nil {
		return nil, mp4Fault(path, "tfhd", err)
	} else if len(boxes) > 0 {
		if tfhd, ok := boxes[0].Payload.(*mp4.Tfhd); ok {
			out.TrackID = tfhd.TrackID
		}
	}

	var baseTicks uint64
	if boxes, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()}); err != nil {
		return nil, mp4Fault(path, "tfdt", err)
	} else if len(boxes) > 0 {
		if tfdt, ok := boxes[0].Payload.(*mp4.Tfdt); ok {
			baseTicks = tfdt.GetBaseMediaDecodeTime()
		}
	}
	if baseTicks > math.MaxInt64 {
		return nil, &Fault{Phase: PhaseMP4, Code: CodeTimeOverflow, Source: path, Detail: "tfdt base exceeds int64"}
	}
	baseNS, err := DurationFromTimescale(int64(baseTicks), timescale)
	if err != nil {
		f := WrapFault(PhaseMP4, CodeTimeOverflow, path, err)
		f.Offset = 0
		return nil, f
	}
	out.BaseNS = baseNS

	truns, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTrun()})
	if err != nil {
		return nil, mp4Fault(path, "trun", err)
	}
	if len(truns) == 0 {
		return nil, &Fault{Phase: PhaseMP4, Code: CodeInvalidMP4, Source: path, Detail: "missing trun box"}
	}

	payloadStart, payloadEnd, err := mdatRange(f, path, st.Size())
	if err != nil {
		return nil, err
	}
	out.PayloadEnd = payloadEnd
	cursor := payloadStart
	ticks := int64(baseTicks)
	index := 0
	for _, twp := range truns {
		trun, ok := twp.Payload.(*mp4.Trun)
		if !ok {
			continue
		}
		for _, e := range trun.Entries {
			durNS, derr := DurationFromTimescale(int64(e.SampleDuration), timescale)
			if derr != nil {
				return nil, WrapFault(PhaseMP4, CodeTimeOverflow, path, derr)
			}
			ptsNS, perr := DurationFromTimescale(ticks, timescale)
			if perr != nil {
				return nil, WrapFault(PhaseMP4, CodeTimeOverflow, path, perr)
			}
			end := cursor + int64(e.SampleSize)
			if end > payloadEnd {
				missingAt := max64(cursor, payloadEnd)
				return nil, &Fault{Phase: PhaseMP4, Code: CodeTruncatedMP4, Source: path,
					Offset: payloadEnd,
					Detail: fmt.Sprintf("sample %d declares %d bytes; sample_start=%d declared_end=%d truncation_offset=%d",
						index, e.SampleSize, cursor, end, missingAt)}
			}
			out.Samples = append(out.Samples, Sample{Index: index, PTSNS: ptsNS, DurationNS: durNS,
				Size: e.SampleSize, DataOffset: cursor})
			cursor = end
			ticks += int64(e.SampleDuration)
			index++
		}
	}
	if len(out.Samples) == 0 {
		return nil, &Fault{Phase: PhaseMP4, Code: CodeInvalidMP4, Source: path, Detail: "trun contains no samples"}
	}
	return out, nil
}

func mdatRange(f *os.File, path string, fileSize int64) (int64, int64, error) {
	boxes, err := mp4.ExtractBox(f, nil, mp4.BoxPath{mp4.BoxTypeMdat()})
	if err != nil {
		return 0, 0, mp4Fault(path, "mdat", err)
	}
	if len(boxes) == 0 {
		return 0, 0, &Fault{Phase: PhaseMP4, Code: CodeInvalidMP4, Source: path, Detail: "missing mdat box"}
	}
	b := boxes[0]
	start := int64(b.Offset + b.HeaderSize)
	end := int64(b.Offset + b.Size)
	if b.ExtendToEOF || end > fileSize {
		end = fileSize
	}
	return start, end, nil
}

func mp4Fault(path, box string, err error) error {
	return &Fault{Phase: PhaseMP4, Code: CodeInvalidMP4, Source: path, Detail: "reading " + box, Err: err}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
