// Package demux streams MP4/MOV sample tables without loading media payloads
// into memory. Only box headers and the compact sample tables are read up
// front; sample bytes stay on disk and are fetched on demand through the
// returned Sample locations.
package demux

import (
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"
	"github.com/zld/continuity/internal/clock"
)

// Sample is one decoded table row. FileOffset points at the first byte of the
// sample inside the source and Size is its length. DTS is the exact decode
// time on the unified clock; Duration uses the same timescale.
type Sample struct {
	Index      uint32 // zero-based within the track
	DTS        clock.Rational
	Duration   clock.Rational
	FileOffset uint64
	Size       uint32
	Sync       bool
	// Present is false when the container could not declare a DTS for this row
	// and the repair pass must continue from the previous sample.
	Present bool
}

// Track is one parsed trak with its full sample location table.
type Track struct {
	ID        uint32
	Timescale uint32
	Video     bool
	Samples   []Sample
	// SyncRows are one-based sample numbers as stored in stss.
	SyncRows []uint32
}

// DurationTicks sums every sample duration on the track timescale.
func (t *Track) DurationTicks() uint64 {
	var sum uint64
	for _, s := range t.Samples {
		d, err := s.Duration.TicksExpr(int64(t.Timescale))
		if err != nil {
			return sum
		}
		sum += uint64(d)
	}
	return sum
}

// File is an opened container. Close releases the underlying descriptor.
type File struct {
	path   string
	rs     io.ReadSeeker
	closer io.Closer
	Tracks []*Track
}

// Open parses the sample tables of path and returns a seekable handle.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.Wrap(clock.StageDemux, "open", err)
	}
	parsed, err := parse(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	parsed.path = path
	parsed.rs = f
	parsed.closer = f
	return parsed, nil
}

// Path reports the source location.
func (f *File) Path() string { return f.path }

// Close releases the file descriptor.
func (f *File) Close() error {
	if f.closer == nil {
		return nil
	}
	return f.closer.Close()
}

// ReadSample fetches one sample's bytes on demand.
func (f *File) ReadSample(track, index uint32) ([]byte, error) {
	if int(track) >= len(f.Tracks) {
		return nil, clock.NewError(clock.StageDemux, "read-sample",
			fmt.Errorf("%w: %v", clock.ErrCorrupt, errTrackNotFound)).
			WithTrack(track).WithSample(index)
	}
	tr := f.Tracks[track]
	if int(index) >= len(tr.Samples) {
		return nil, clock.NewError(clock.StageDemux, "read-sample",
			fmt.Errorf("%w: %v", clock.ErrCorrupt, errSampleOutOfRange)).
			WithTrack(track).WithSample(index)
	}
	s := tr.Samples[index]
	buf := make([]byte, s.Size)
	if _, err := f.rs.Seek(int64(s.FileOffset), io.SeekStart); err != nil {
		return nil, clock.Wrap(clock.StageDemux, "seek", err)
	}
	if _, err := io.ReadFull(f.rs, buf); err != nil {
		return nil, clock.NewError(clock.StageDemux, "read", err).
			WithTrack(track).WithSample(index)
	}
	return buf, nil
}

// boxPath is a small helper for go-mp4 path construction.
func boxPath(types ...mp4.BoxType) []mp4.BoxType { return types }
