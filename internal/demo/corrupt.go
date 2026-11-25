package demo

import (
	"os"
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/fixture"
	"mediacore/internal/index"
	"mediacore/internal/mp4"
)

// ErrorResult is one corrupt/boundary acceptance row.
type ErrorResult struct {
	Name     string `json:"name"`
	Expected string `json:"expected_category"`
	Category string `json:"category"`
	Path     string `json:"path"`
	Track    int    `json:"track_number"`
	Offset   int64  `json:"raw_offset"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail,omitempty"`
}

// CorruptReport contains all negative-case diagnostics.
type CorruptReport struct {
	Results        []ErrorResult `json:"results"`
	VerifiedMarker bool          `json:"verified_marker"`
}

// RunCorrupt generates and exercises every required corruption/boundary class.
func RunCorrupt(workdir string, r index.Range) (*CorruptReport, error) {
	_ = os.RemoveAll(workdir)
	cases, err := fixture.GenerateCorrupt(workdir)
	if err != nil {
		return nil, err
	}
	rep := &CorruptReport{}
	for _, c := range cases {
		got := exerciseCase(c, r)
		rep.Results = append(rep.Results, got)
	}
	rep.Results = append(rep.Results, boundaryCases(workdir, r)...)
	marker := filepath.Join(workdir, "VERIFIED")
	if _, err := os.Stat(marker); err == nil {
		rep.VerifiedMarker = true
	}
	for _, row := range rep.Results {
		if !row.OK {
			return rep, clock.New(clock.CategoryInvalidArgument, row.Path, row.Track, row.Offset,
				"corrupt acceptance assertion failed for "+row.Name)
		}
	}
	if rep.VerifiedMarker {
		return rep, clock.New(clock.CategoryInvalidArgument, marker, 0, 0,
			"corrupt batch unexpectedly produced a verified marker")
	}
	return rep, nil
}

func exerciseCase(c fixture.CorruptCase, r index.Range) ErrorResult {
	var err error
	switch c.Kind {
	case "mp4":
		_, err = mp4.ParseFile(c.Path)
	case "rtp":
		_, err = mp4.ParseRTP(c.Path, c.TrackNumber, 90000)
	case "intervals":
		_, err = index.LoadIntervalFile(c.Path)
	case "missing-keyframe":
		err = checkMissingKeyframe(c)
	default:
		err = clock.New(clock.CategoryInvalidArgument, c.Path, c.TrackNumber, 0, "unknown corrupt case kind")
	}
	return classify(c.Name, c.Category, c.Path, err)
}

func checkMissingKeyframe(c fixture.CorruptCase) error {
	file, err := mp4.ParseFile(c.Path)
	if err != nil {
		return err
	}
	if c.TrackNumber < 1 || c.TrackNumber > len(file.Tracks) {
		return clock.New(clock.CategoryMissingTrack, c.Path, c.TrackNumber, 0, "track not found")
	}
	idx, err := index.Build("corrupt", file.Size(), file.Tracks[c.TrackNumber-1])
	if err != nil {
		return err
	}
	return idx.VerifyExpectedKeyframes(c.ExpectedEvery, c.TotalSamples)
}

func boundaryCases(workdir string, r index.Range) []ErrorResult {
	missingTrackErr := func() error {
		_, err := index.QueryNamed(map[string]*index.Index{}, []string{"absent-track"}, r)
		return err
	}()
	outOfRangeErr := func() error {
		idx := &index.Index{Path: filepath.Join(workdir, "boundary.mp4"), Track: 1}
		tm, err := clock.NewIdentityMap(idx.Path, 1, 1000, 0, 10_000)
		if err != nil {
			return err
		}
		idx.TimeMap = tm
		idx.TimeScale = 1000
		idx.Keyframes = nil
		_, err = idx.Query(index.Range{Start: -2_000_000_000, End: -1_000_000_000})
		return err
	}()
	timebaseErr := index.CheckTimebases([]*index.Index{{Path: "incompatible-timebase", Track: 9}})
	return []ErrorResult{
		classify("missing track", string(clock.CategoryMissingTrack), "multi-track-query", missingTrackErr),
		classify("event range before first keyframe", string(clock.CategoryOutOfRange), "multi-track-query", outOfRangeErr),
		classify("incompatible timebase", string(clock.CategoryTimebase), "multi-track-query", timebaseErr),
	}
}

func classify(name, expected, fallbackPath string, err error) ErrorResult {
	row := ErrorResult{Name: name, Expected: expected, Path: fallbackPath}
	if err == nil {
		row.Category = "unexpected_success"
		row.Detail = "expected an error"
		return row
	}
	ce, ok := clock.As(err)
	if !ok {
		row.Category = string(clock.CategoryInvalidArgument)
		row.Detail = err.Error()
		return row
	}
	row.Category = string(ce.Category)
	row.Path = ce.Path
	row.Track = ce.Track
	row.Offset = ce.Offset
	if row.Offset < 0 {
		row.Offset = 0
	}
	row.OK = row.Category == expected && row.Offset >= 0
	if !row.OK {
		row.Detail = err.Error()
	}
	return row
}
