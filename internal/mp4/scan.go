// Package mp4 scans an MP4's sample tables and edit list without loading
// media payloads. It validates untrusted size fields, bounds the number of
// samples, and records the raw byte offset of every table so errors can be
// re-checked with a hex dump.
package mp4

import (
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"mediad/internal/clock"
)

// ScanFile opens path and scans it. The file is read with seeking only, so
// memory is bounded by sample count, never by media size.
func ScanFile(path string) (*Scan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.New(clock.StageScan, clock.CodeIO, "open %s: %v", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, clock.New(clock.StageScan, clock.CodeIO, "stat %s: %v", path, err)
	}
	return ScanReader(f, path, st.Size())
}

// ScanReader scans the MP4 in r, whose total size is known.
func ScanReader(r io.ReadSeeker, path string, size int64) (*Scan, error) {
	if size < 0 {
		return nil, clock.New(clock.StageScan, clock.CodeTruncatedTable, "negative file size")
	}
	s := &Scan{Path: path, Size: size}
	// Validate the top-level box chain first so truncation is reported with a
	// byte offset rather than an opaque parser error.
	if err := walkTopLevel(r, size); err != nil {
		return nil, err
	}
	if err := validateTables(r, size); err != nil {
		return nil, err
	}
	if err := scanMovie(r, s); err != nil {
		return nil, err
	}
	if len(s.Tracks) == 0 {
		return nil, clock.New(clock.StageScan, clock.CodeBadManifest, "no tracks found in %s", path)
	}
	return s, nil
}

// walkTopLevel verifies that every box header fits inside the file.
func walkTopLevel(r io.ReadSeeker, size int64) error {
	var off int64
	for off < size {
		if size-off < 8 {
			return clock.New(clock.StageScan, clock.CodeTruncatedTable,
				"trailing %d bytes are too short for a box header", size-off).WithOffset(off)
		}
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return clock.New(clock.StageScan, clock.CodeIO, "%v", err).WithOffset(off)
		}
		var hdr [16]byte
		n, err := io.ReadFull(r, hdr[:8])
		if err != nil || n < 8 {
			return clock.New(clock.StageScan, clock.CodeTruncatedTable, "short box header").WithOffset(off)
		}
		boxSize := int64(beU32(hdr[0:4]))
		typ := string(hdr[4:8])
		header := int64(8)
		if boxSize == 1 {
			if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
				return clock.New(clock.StageScan, clock.CodeTruncatedTable, "short 64-bit box header").WithOffset(off)
			}
			boxSize = int64(beU64(hdr[8:16]))
			header = 16
		} else if boxSize == 0 {
			boxSize = size - off
		}
		if boxSize < header {
			return clock.New(clock.StageScan, clock.CodeTruncatedTable,
				"box %q at offset %d declares size %d below header size %d", typ, off, boxSize, header).WithOffset(off)
		}
		if boxSize > size-off {
			return clock.New(clock.StageScan, clock.CodeTruncatedTable,
				"box %q at offset %d declares %d bytes but only %d remain", typ, off, boxSize, size-off).WithOffset(off)
		}
		if typ == "mdat" && boxSize-header > MaxMdatBytes {
			return clock.New(clock.StageScan, clock.CodeOversizedTable,
				"mdat declares %d bytes", boxSize-header).WithOffset(off)
		}
		off += boxSize
	}
	return nil
}

// validateTables bound-checks every stbl child before typed parsing.
func validateTables(r io.ReadSeeker, size int64) error {
	stbls, err := findStbl(r, size)
	if err != nil {
		return err
	}
	for _, st := range stbls {
		if err := validateSampleTable(r, st, st.TrackID); err != nil {
			return err
		}
	}
	return nil
}

func scanMovie(r io.ReadSeeker, s *Scan) error {
	// mvhd gives the movie timescale used by elst durations.
	mvhds, err := gomp4.ExtractBoxesWithPayload(r, nil, []gomp4.BoxPath{p("moov", "mvhd")})
	if err != nil {
		return wrapParse(err, -1, 0)
	}
	if len(mvhds) != 1 {
		return clock.New(clock.StageScan, clock.CodeBadManifest, "expected exactly one mvhd, found %d", len(mvhds))
	}
	mvhd := mvhds[0].Payload.(*gomp4.Mvhd)
	if mvhd.Timescale == 0 {
		return clock.New(clock.StageScan, clock.CodeBadTimescale, "movie timescale is zero").WithOffset(int64(mvhds[0].Info.Offset))
	}
	s.MovieScale = mvhd.Timescale

	// Enumerate all traks under moov to learn count first (bounded).
	traks, err := gomp4.ExtractBoxes(r, nil, []gomp4.BoxPath{p("moov", "trak")})
	if err != nil {
		return wrapParse(err, -1, 0)
	}
	if len(traks) > MaxTracks {
		return clock.New(clock.StageScan, clock.CodeOversizedTable, "%d tracks exceeds limit %d", len(traks), MaxTracks)
	}

	for i := range traks {
		trak := traks[i]
		tr := RawTrack{MovieTimescale: mvhd.Timescale}
		trackOffset := int64(trak.Offset)
		if err := readTrackHeader(r, trak, &tr); err != nil {
			return err
		}
		if err := readMediaHeader(r, trak, &tr); err != nil {
			return err
		}
		if err := readEditList(r, trak, &tr); err != nil {
			return err
		}
		if err := readSampleTables(r, trak, &tr); err != nil {
			return err
		}
		_ = trackOffset
		s.Tracks = append(s.Tracks, tr)
	}
	return nil
}
