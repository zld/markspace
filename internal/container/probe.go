// Package container parses local MP4/MOV containers with go-mp4 and validates
// track time bases against the shared integer-nanosecond clock basis.
package container

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"replaycheck/internal/clock"
)

// MaxFileBytes bounds all local reads. Fixtures are tiny; this also prevents a
// corrupt size field or an accidentally huge input from causing unbounded
// memory allocation.
const MaxFileBytes = 2 << 20 // 2 MiB

// FileReport is the container subsystem's validation record.
type FileReport struct {
	Path             string         `json:"path"`
	Size             int64          `json:"size"`
	MajorBrand       string         `json:"major_brand"`
	MovieTimescale   uint32         `json:"movie_timescale"`
	MovieDurationRaw uint64         `json:"movie_duration_raw"`
	MovieTimebase    clock.Timebase `json:"movie_timebase"`
	Tracks           []TrackReport  `json:"tracks"`
	MoovOffset       int64          `json:"moov_offset"`
}

// TrackReport retains original and converted time bases plus frame DTS data.
type TrackReport struct {
	ID            uint32         `json:"track_id"`
	Codec         string         `json:"codec"`
	Timebase      clock.Timebase `json:"timebase"`
	SampleCount   int            `json:"sample_count"`
	Timeline      clock.Timeline `json:"timeline"`
	DriftNS       int64          `json:"drift_ns"`
	DriftMS       float64        `json:"drift_ms"`
	DriftExceeded bool           `json:"drift_exceeded"`
	StblOffset    int64          `json:"stbl_offset"`
}

// ProbePath checks readability and size before reading at most MaxFileBytes+1.
func ProbePath(path string) (*FileReport, []clock.Anomaly, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, clock.NewError(clock.CodeUnreadable, err.Error(), -1).WithPath(path)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, nil, clock.NewError(clock.CodeUnreadable, err.Error(), -1).WithPath(path)
	}
	if !st.Mode().IsRegular() {
		return nil, nil, clock.NewError(clock.CodeUnreadable, "input is not a regular file", -1).WithPath(path)
	}
	if st.Size() > MaxFileBytes {
		return nil, nil, clock.NewError(clock.CodeTooLarge,
			fmt.Sprintf("file size %d exceeds limit %d", st.Size(), MaxFileBytes), -1).WithPath(path)
	}

	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, nil, clock.NewError(clock.CodeUnreadable, err.Error(), -1).WithPath(path)
	}
	if int64(len(data)) > MaxFileBytes {
		return nil, nil, clock.NewError(clock.CodeTooLarge,
			fmt.Sprintf("file exceeds read limit %d", MaxFileBytes), -1).WithPath(path)
	}
	return ProbeBytes(path, data)
}

// ProbeBytes performs header/table preflight, then uses go-mp4 for the
// standards-based box and track extraction.
func ProbeBytes(path string, data []byte) (*FileReport, []clock.Anomaly, error) {
	pre, perr := preflight(data)
	if perr != nil {
		perr.Path = path
		return nil, nil, perr
	}

	info, err := gomp4.Probe(bytes.NewReader(data))
	if err != nil {
		ce := clock.NewError(clock.CodeBadHeader, "go-mp4 probe failed: "+err.Error(), pre.moovOffset)
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			ce.Code = clock.CodeTruncated
			if ce.Offset < 0 {
				ce.Offset = int64(len(data))
			}
		}
		ce.Path = path
		return nil, nil, ce
	}

	report := &FileReport{
		Path: path, Size: int64(len(data)), MajorBrand: pre.brand,
		MovieTimescale: info.Timescale, MovieDurationRaw: info.Duration,
		MoovOffset: pre.moovOffset, Tracks: []TrackReport{},
	}
	if info.Timescale != 0 {
		tb, err := clock.NewTimebase(info.Timescale, 0, int64(info.Duration))
		if err != nil {
			ce := clock.AsError(err)
			ce.Path = path
			return nil, nil, ce
		}
		report.MovieTimebase = tb
	}

	anomalies := []clock.Anomaly{}
	for i, t := range info.Tracks {
		stblOffset := int64(-1)
		if i < len(pre.tracks) {
			stblOffset = pre.tracks[i].stblOffset
		}
		tr, trackAnomalies, err := buildTrack(t, report.MovieTimebase, stblOffset)
		if err != nil {
			ce := clock.AsError(err)
			ce.Path = path
			anomalies = append(anomalies, clock.AnomalyFromError(ce, "failure"))
			return report, anomalies, ce
		}
		report.Tracks = append(report.Tracks, tr)
		anomalies = append(anomalies, trackAnomalies...)
	}
	if len(report.Tracks) == 0 {
		ce := clock.NewError(clock.CodeMissingTrack, "no track tables found", pre.moovOffset).WithPath(path)
		return nil, anomalies, ce
	}
	return report, anomalies, nil
}
