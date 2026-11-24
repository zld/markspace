// Package pipeline generates the demo fixtures and runs the full archive
// kernel: probe, normalise, compose, transport recovery, fan-in, render and
// manifest.
package pipeline

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"archive/internal/clock"
	"archive/internal/fixture"
)

// AssetPaths are the generated fixture files.
type AssetPaths struct {
	NormalMP4       string
	NormalMXF       string
	BoundaryMP4     string
	CorruptMP4      string
	CorruptInfo     TruncationInfo
	BoundaryMP4Name string
	InvalidMP4      string
	BoundaryClip    BoundarySpec
}

// TruncationInfo describes where a deliberately truncated file stops.
type TruncationInfo struct {
	FileSize        int64  `json:"file_size"`
	TruncatedAtByte int64  `json:"truncated_at_byte"`
	DeclaredEnd     int64  `json:"declared_end_byte"`
	Box             string `json:"box"`
	ExitCode        int    `json:"exit_code"`
}

// BoundarySpec records the exact grid the boundary fixture uses.
type BoundarySpec struct {
	FrameIntervalNS int64 `json:"frame_interval_ns"`
	StartNS         int64 `json:"start_ns"`
	EndNS           int64 `json:"end_ns"`
}

// Video geometry and timing shared by every sample.
const (
	FrameWidth     = 960
	FrameHeight    = 540
	FrameInterval  = int64(40_000_000) // 25 fps
	FrameCount     = 6
	MovieTimescale = uint32(1000)
)

// Generate writes normal, boundary and corrupt fixtures into dir.
func Generate(dir string) (*AssetPaths, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	_ = dir
	p := &AssetPaths{
		NormalMP4:   filepath.Join(dir, "normal.mp4"),
		NormalMXF:   filepath.Join(dir, "normal.mxf"),
		BoundaryMP4: filepath.Join(dir, "boundary.mp4"),
		CorruptMP4:  filepath.Join(dir, "corrupt.mp4"),
		InvalidMP4:  filepath.Join(dir, "invalid-crop.mp4"),
	}
	normal := fixture.MP4Spec{
		MovieTimescale: MovieTimescale,
		Tracks: []fixture.MP4TrackSpec{
			{
				ID: "cam-a", Kind: "video",
				StoredWidth: FrameWidth, StoredHeight: FrameHeight,
				DisplayWidth: FrameWidth, DisplayHeight: FrameHeight,
				Timescale: 25, SampleCount: FrameCount, SampleDuration: 1,
				Rotation: 0,
				Clap:     &fixture.MP4Clap{Width: 720, Height: 540, HorizOff: -120},
				Colrs: []fixture.MP4Colr{
					{Primaries: 1, Transfer: 1, Matrix: 1},                   // BT.709
					{Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true}, // BT.2020 conflict
				},
			},
			{
				ID: "cam-b", Kind: "video",
				StoredWidth: FrameWidth, StoredHeight: FrameHeight,
				DisplayWidth: FrameHeight, DisplayHeight: FrameWidth,
				Timescale: 25, SampleCount: FrameCount, SampleDuration: 1,
				Rotation: 90,
				Colrs:    []fixture.MP4Colr{{Primaries: 1, Transfer: 1, Matrix: 1}},
			},
		},
	}
	if err := os.WriteFile(p.NormalMP4, fixture.BuildMP4(normal), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", p.NormalMP4, err)
	}

	mxf := fixture.MXFSpec{Tracks: []fixture.MXFTrackSpec{{
		ID: "mxf-cam", StoredWidth: FrameWidth, StoredHeight: FrameHeight,
		DisplayWidth: FrameWidth, DisplayHeight: FrameHeight,
		RateNum: 25, RateDen: 1, FrameCount: FrameCount,
		Rotation: 180,
		Crop:     &fixture.MXFCrop{Left: 0, Right: 720, Top: 0, Bottom: 540},
		Color:    &fixture.MXFColor{Primaries: 1, Transfer: 1, Matrix: 1},
		Clips:    []fixture.MXFClip{{StartFrame: 0, FrameCount: FrameCount}},
	}}}
	if err := os.WriteFile(p.NormalMXF, fixture.BuildMXF(mxf), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", p.NormalMXF, err)
	}

	boundary := fixture.MP4Spec{
		MovieTimescale: MovieTimescale,
		Tracks: []fixture.MP4TrackSpec{{
			ID: "edge", Kind: "video",
			StoredWidth: FrameWidth, StoredHeight: FrameHeight,
			DisplayWidth: FrameWidth, DisplayHeight: FrameHeight,
			Timescale: 25, SampleCount: FrameCount, SampleDuration: 1,
			Rotation: 0,
		}},
	}
	if err := os.WriteFile(p.BoundaryMP4, fixture.BuildMP4(boundary), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", p.BoundaryMP4, err)
	}
	p.BoundaryClip = BoundarySpec{
		FrameIntervalNS: FrameInterval,
		StartNS:         FrameInterval, // exactly one frame in
		EndNS:           FrameInterval * FrameCount,
	}

	invalid := fixture.MP4Spec{
		MovieTimescale: MovieTimescale,
		Tracks: []fixture.MP4TrackSpec{{
			ID: "bad-crop", Kind: "video",
			StoredWidth: FrameWidth, StoredHeight: FrameHeight,
			DisplayWidth: FrameWidth, DisplayHeight: FrameHeight,
			Timescale: 25, SampleCount: FrameCount, SampleDuration: 1,
			Clap: &fixture.MP4Clap{Width: FrameWidth * 2, Height: FrameHeight},
		}},
	}
	if err := os.WriteFile(p.InvalidMP4, fixture.BuildMP4(invalid), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", p.InvalidMP4, err)
	}

	corrupt := normal
	corrupt.TruncateBytes = 96
	data := fixture.BuildMP4(corrupt)
	if err := os.WriteFile(p.CorruptMP4, data, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", p.CorruptMP4, err)
	}
	p.CorruptInfo = truncationInfo(data)
	return p, nil
}

// truncationInfo finds the first root box whose declared end passes EOF.
func truncationInfo(data []byte) TruncationInfo {
	info := TruncationInfo{FileSize: int64(len(data)), TruncatedAtByte: int64(len(data)), ExitCode: 3}
	r := newReadSeeker(data)
	for {
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			break
		}
		if int64(bi.Offset+bi.Size) > int64(len(data)) {
			info.TruncatedAtByte = int64(bi.Offset)
			info.DeclaredEnd = int64(bi.Offset + bi.Size)
			info.Box = bi.Type.String()
			break
		}
		if _, err := bi.SeekToEnd(r); err != nil {
			break
		}
	}
	return info
}

// ClassifyExit maps an error to a stable process exit code.
func ClassifyExit(err error) int {
	if err == nil {
		return 0
	}
	switch clock.AsKind(err) {
	case clock.KindTruncated:
		return 3
	case clock.KindMisaligned:
		return 4
	case clock.KindUnsupported, clock.KindInvalidMetadata, clock.KindOverflow:
		return 5
	}
	return 4
}

// newReadSeeker wraps a byte slice in the minimal io.ReadSeeker go-mp4 needs.
func newReadSeeker(b []byte) *sliceSeeker { return &sliceSeeker{b: b} }

type sliceSeeker struct {
	b   []byte
	pos int64
}

func (s *sliceSeeker) Read(p []byte) (int, error) {
	if s.pos >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[s.pos:])
	s.pos += int64(n)
	return n, nil
}

func (s *sliceSeeker) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = off
	case io.SeekCurrent:
		s.pos += off
	case io.SeekEnd:
		s.pos = int64(len(s.b)) + off
	}
	if s.pos < 0 {
		return 0, fmt.Errorf("negative seek")
	}
	return s.pos, nil
}
