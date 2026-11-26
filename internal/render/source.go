package render

import (
	"encoding/binary"
	"io"
	"os"

	"replayctl/internal/fault"
	"replayctl/internal/index"
	mp4scan "replayctl/internal/mp4"
)

// Source is an opened, indexed MP4 source.
type Source struct {
	Path  string
	File  *os.File
	Movie *mp4scan.Movie
	Index *index.Index
	pcm   []int16
}

// OpenSource scans, indexes, and opens path for random reads.
func OpenSource(path string) (*Source, error) {
	mov, err := mp4scan.ScanFile(path)
	if err != nil {
		return nil, err
	}
	idx, err := index.Build(mov)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.New(fault.IO, fault.StageRender, path, -1, -1, "open media: %v", err)
	}
	return &Source{Path: path, File: f, Movie: mov, Index: idx}, nil
}

// Close releases the source file.
func (s *Source) Close() error {
	if s == nil || s.File == nil {
		return nil
	}
	return s.File.Close()
}

// PCM16 returns all mono s16le samples for the audio track, loading once.
func (s *Source) PCM16() ([]int16, error) {
	if s.pcm != nil {
		return s.pcm, nil
	}
	tr := s.Movie.Track("audio")
	if tr == nil || len(tr.Samples) == 0 {
		return nil, fault.New(fault.Unsupported, fault.StageRender, s.Path, -1, -1, "no audio samples")
	}
	first := tr.Samples[0]
	last := tr.Samples[len(tr.Samples)-1]
	end := last.Offset + last.Size
	buf := make([]byte, end-first.Offset)
	if _, err := io.ReadFull(io.NewSectionReader(s.File, first.Offset, end-first.Offset), buf); err != nil {
		return nil, fault.Wrap(err, fault.IO, fault.StageRender, s.Path, first.Offset, first.DTS)
	}
	if len(buf)%2 != 0 {
		return nil, fault.New(fault.InvalidBox, fault.StageRender, s.Path, first.Offset, first.DTS, "PCM byte length %d odd", len(buf))
	}
	s.pcm = make([]int16, len(buf)/2)
	for i := range s.pcm {
		s.pcm[i] = int16(binary.LittleEndian.Uint16(buf[2*i : 2*i+2]))
	}
	return s.pcm, nil
}

// ReadJPEG reads one video sample by zero-based sample index.
func (s *Source) ReadJPEG(sampleIndex int) ([]byte, error) {
	return s.Movie.ReadSampleAt(s.File, "video", sampleIndex)
}
