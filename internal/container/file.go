package container

import (
	"fmt"
	"os"

	"mediacore/internal/core"
)

// Scan is the result of reading a container's structure and audio track
// metadata. It is intentionally free of recovery decisions.
type Scan struct {
	File     string
	FileSize int64
	Root     Box
	BoxCount int
	Mdat     []Box
	Tracks   []TrackScan
	Warnings []*core.Error
	FTyp     string
	Limits   Limits

	handle *os.File
}

var containerBoxes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "edts": true, "dinf": true, "udta": true, "mvex": true,
}

// containerExtra is the distance from payload start to the first child box for
// containers that prefix children with fixed fields.
var containerExtra = map[string]int64{
	"stsd": 8, // FullBox + entry count
	"meta": 4, // FullBox
}

func isContainer(typ string) bool {
	return containerBoxes[typ] || containerExtra[typ] != 0
}

// ScanFile reads path under lim. Structural damage never aborts the scan:
// truncated regions are recorded and the readable prefix is returned.
func ScanFile(path string, lim Limits) (*Scan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, core.Wrap(core.CodeIO, core.StageScan, false, "open container", err).WithOffset(0)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, core.Wrap(core.CodeIO, core.StageScan, false, "stat container", err)
	}
	size := st.Size()
	if lim.MaxFileBytes > 0 && size > lim.MaxFileBytes {
		return nil, core.New(core.CodeLimitExceeded, core.StageScan, false,
			fmt.Sprintf("file size %d exceeds limit %d", size, lim.MaxFileBytes))
	}

	s := &Scan{File: path, FileSize: size, Limits: lim}
	state := &scanState{file: f, size: size, lim: lim}
	root := &Box{Type: "file", Offset: 0, Size: size}
	children, cerr := state.children(0, size, 1)
	root.Children = children
	s.Root = *root
	s.BoxCount = state.count
	if s.BoxCount == 0 {
		s.warn(core.New(core.CodeBoxTruncated, core.StageScan, false, "no readable box header"))
	}
	if cerr != nil && cerr.Code != core.CodeBoxTruncated {
		s.warn(cerr)
	}

	// Locate moov; the box may appear after mdat in non-faststart files.
	var moov *Box
	for _, c := range root.Children {
		switch c.Type {
		case "ftyp":
			s.FTyp = readFTyp(f, c)
		case "mdat":
			s.Mdat = append(s.Mdat, *c)
		case "moov":
			moov = c
		}
	}
	s.Limits = lim
	if moov == nil {
		s.warn(core.New(core.CodeMoovMissing, core.StageScan, false,
			"moov not readable; audio index cannot be trusted"))
		return s, nil
	}
	if err := s.readTracks(moov); err != nil {
		s.warn(err)
	}
	return s, nil
}

func (s *Scan) warn(e *core.Error) { s.Warnings = append(s.Warnings, e) }
