package validate

import (
	"encoding/binary"
	"io"
	"os"

	"replayctl/internal/fault"
	mp4scan "replayctl/internal/mp4"
)

func readPCM(mov *mp4scan.Movie, f *os.File, path string) ([]int16, error) {
	tr := mov.Track("audio")
	if tr == nil || len(tr.Samples) == 0 {
		return nil, fault.New(fault.Unsupported, fault.StageValidate, path, -1, -1, "no audio track")
	}
	first := tr.Samples[0]
	last := tr.Samples[len(tr.Samples)-1]
	end := last.Offset + last.Size
	buf := make([]byte, end-first.Offset)
	n, err := io.ReadFull(io.NewSectionReader(f, first.Offset, end-first.Offset), buf)
	if err != nil {
		off := first.Offset + int64(n)
		return nil, fault.New(fault.SampleOffset, fault.StageValidate, path, off, first.DTS, "read PCM range: %v", err)
	}
	if len(buf)%2 != 0 {
		return nil, fault.New(fault.Validation, fault.StageValidate, path, first.Offset, first.DTS, "odd PCM byte length")
	}
	pcm := make([]int16, len(buf)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(buf[2*i : 2*i+2]))
	}
	return pcm, nil
}
