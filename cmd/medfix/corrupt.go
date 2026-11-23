package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"medfix/internal/fixture"
	"medfix/internal/pipeline"
	"medfix/internal/timeline"
)

func tempPath(name string) string {
	return filepath.Join(os.TempDir(), name)
}

func fmtPrintf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

// reportCorruptSamples runs the truncated-MP4 and ambiguous-RTP samples and
// prints the raw offset each structured error carries. These samples must
// report a concrete source offset rather than aborting without location.
func reportCorruptSamples(seed int64) {
	mp4 := fixture.GenerateMP4(seed, []fixture.MP4TrackSpec{
		{TrackID: 1, Timescale: 90000, Samples: 12, Delta: 3000, KeyEvery: 2, PayloadBase: 1},
	})
	cut := len(mp4.Bytes) - 24
	corrupt := []pipeline.Input{{ID: "mp4-truncated", Kind: "mp4",
		Data: fixture.TruncateAt(mp4.Bytes, cut)}}
	if _, err := runInputs(tempPath("medfix-corrupt-mp4"), corrupt); err != nil {
		fmtPrintf("corrupt=mp4_truncated source_offset=%s\n", offsetOf(err))
	}

	rtp := fixture.GenerateRTP(seed, fixture.RTPSpec{
		ClockRate: 90000, Packets: 4, StartSeq: 0,
		StartTS: 200000, TSIncrement: 3000,
	})
	amb := injectAmbiguity(rtp.Bytes)
	ambInput := []pipeline.Input{{ID: "rtp-ambiguous", Kind: "rtp", Data: amb, ClockRate: 90000}}
	if _, err := runInputs(tempPath("medfix-corrupt-rtp"), ambInput); err != nil {
		fmtPrintf("corrupt=rtp_rollover_ambiguous source_offset=%s\n", offsetOf(err))
	}
}

func runInputs(dir string, inputs []pipeline.Input) (*pipeline.Report, error) {
	r, err := pipeline.New("medfix-corrupt", inputs,
		pipeline.Options{WorkDir: dir, Config: timeline.DefaultConfig()})
	if err != nil {
		return nil, err
	}
	return r.Execute()
}

func offsetOf(err error) string {
	var te *timeline.Error
	if errors.As(err, &te) {
		return strconv.FormatInt(te.Offset, 10)
	}
	return "unknown"
}

// injectAmbiguity walks a length-prefixed RTP capture and rewrites sequence
// 1's timestamp below sequence 0's value, producing an undecidable backward
// jump that the unwrapper must reject with an offset-carrying error.
func injectAmbiguity(capture []byte) []byte {
	out := append([]byte(nil), capture...)
	var off int
	for off+4 <= len(out) {
		frameLen := int(binary.BigEndian.Uint32(out[off : off+4]))
		frameStart := off + 4
		if frameStart+frameLen > len(out) {
			return out
		}
		seq := binary.BigEndian.Uint16(out[frameStart+2 : frameStart+4])
		if seq == 1 {
			tsPos := frameStart + 4
			orig := binary.BigEndian.Uint32(out[tsPos : tsPos+4])
			binary.BigEndian.PutUint32(out[tsPos:tsPos+4], orig-4000)
			return out
		}
		off = frameStart + frameLen
	}
	return out
}
