package container

import (
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediacore/internal/core"
)

// root reopens the scanned file for payload decoding. The scanner keeps only
// offsets and sizes, so no file descriptor is held across the pipeline.
func (s *Scan) root() *os.File {
	if s.handle == nil {
		h, err := os.Open(s.File)
		if err == nil {
			s.handle = h
		}
	}
	return s.handle
}

// closeFile releases the payload handle if one was opened.
func (s *Scan) closeFile() {
	if s.handle != nil {
		s.handle.Close()
		s.handle = nil
	}
}

func findDirect(parent *Box, typ string) (*Box, bool) {
	for _, c := range parent.Children {
		if c.Type == typ {
			return c, true
		}
	}
	return nil, false
}

// decodeFile unmarshals one box's payload into dst. Any-type boxes (sample
// entries, esds, and others addressed by path) must carry their own type or
// go-mp4 cannot resolve the box definition.
func decodeFile(f *os.File, b *Box, dst mp4.IBox) error {
	if f == nil {
		return core.New(core.CodeIO, core.StageScan, false, "container handle unavailable")
	}
	if b.Truncated {
		return io.ErrUnexpectedEOF
	}
	if any, ok := dst.(mp4.IAnyType); ok {
		any.SetType(mp4.StrToBoxType(b.Type))
	}
	if _, err := f.Seek(b.PayloadOffset(), io.SeekStart); err != nil {
		return err
	}
	_, err := mp4.Unmarshal(f, uint64(b.PayloadSize()), dst, mp4.Context{})
	return err
}
