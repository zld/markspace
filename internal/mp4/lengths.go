package mp4

import (
	"encoding/binary"
	"io"

	"mediad/internal/clock"
)

// boxRef locates one Sample Table Box child so we can bound-check its declared
// length before handing it to the parser.
type boxRef struct {
	Start   uint64
	End     uint64
	Payload uint64
	Type    string
}

// rawBox is a lightweight box header read directly from the file.
type rawBox struct {
	Start      uint64
	Size       uint64
	HeaderSize uint64
	Type       string
	TrackID    uint32 // 0 when the walker did not enter a trak
}

func (b rawBox) payloadStart() uint64 { return b.Start + b.HeaderSize }
func (b rawBox) end() uint64          { return b.Start + b.Size }

// readBoxesFile enumerates direct children of the box at [start,end). It
// rejects size fields that run past the parent or below the header.
func readBoxesFile(r io.ReadSeeker, start, end uint64) ([]rawBox, error) {
	var out []rawBox
	off := start
	for off+8 <= end {
		if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
			return nil, clock.New(clock.StageScan, clock.CodeIO, "%v", err).WithOffset(int64(off))
		}
		var hdr [16]byte
		if _, err := io.ReadFull(r, hdr[:8]); err != nil {
			return nil, clock.New(clock.StageScan, clock.CodeTruncatedTable, "short box header").WithOffset(int64(off))
		}
		size := uint64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		header := uint64(8)
		switch size {
		case 1:
			if off+16 > end {
				return nil, clock.New(clock.StageScan, clock.CodeTruncatedTable, "64-bit header overruns parent").WithOffset(int64(off))
			}
			if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
				return nil, clock.New(clock.StageScan, clock.CodeTruncatedTable, "short 64-bit header").WithOffset(int64(off))
			}
			size = binary.BigEndian.Uint64(hdr[8:16])
			header = 16
		case 0:
			size = end - off
		}
		if size < header {
			return nil, clock.New(clock.StageScan, clock.CodeTruncatedTable,
				"box %q declares size %d below header %d", typ, size, header).WithOffset(int64(off))
		}
		if off+size > end {
			return nil, clock.New(clock.StageScan, clock.CodeTruncatedTable,
				"box %q at %d declares %d bytes, parent ends at %d", typ, off, size, end).WithOffset(int64(off))
		}
		out = append(out, rawBox{Start: off, Size: size, HeaderSize: header, Type: typ})
		off += size
	}
	if off != end {
		return nil, clock.New(clock.StageScan, clock.CodeTruncatedTable,
			"%d trailing bytes cannot form a box", end-off).WithOffset(int64(off))
	}
	return out, nil
}

// validateSampleTable recomputes each table's required byte length from its
// declared entry count and compares it with the space the box actually has.
// This catches truncated indexes before the generic parser reports an opaque
// overrun, and points reviewers at the box header offset.
func validateSampleTable(r io.ReadSeeker, stbl rawBox, trackID uint32) error {
	err := validateSampleTableInner(r, stbl)
	if err == nil {
		return nil
	}
	if pe, ok := clock.As(err); ok && trackID != 0 {
		return pe.WithTrack(trackID)
	}
	return err
}

func validateSampleTableInner(r io.ReadSeeker, stbl rawBox) error {
	children, err := readBoxesFile(r, stbl.payloadStart(), stbl.end())
	if err != nil {
		return err
	}
	for _, b := range children {
		payload := b.end() - b.payloadStart()
		switch b.Type {
		case "stts":
			if err := checkFullTable(r, b, payload, 4+8, 8, "stts"); err != nil {
				return err
			}
		case "stsc":
			if err := checkFullTable(r, b, payload, 4+12, 12, "stsc"); err != nil {
				return err
			}
		case "stss":
			if err := checkFullTable(r, b, payload, 4+4, 4, "stss"); err != nil {
				return err
			}
		case "stco":
			if err := checkFullTable(r, b, payload, 4+4, 4, "stco"); err != nil {
				return err
			}
		case "co64":
			if err := checkFullTable(r, b, payload, 4+8, 8, "co64"); err != nil {
				return err
			}
		case "elst":
			// elst entry size depends on version; read it.
			if payload < 8 {
				return clock.New(clock.StageScan, clock.CodeTruncatedTable, "elst payload smaller than header").WithOffset(int64(b.Start))
			}
			hdr, err := readAt(r, b.payloadStart(), 8)
			if err != nil {
				return err
			}
			entrySize := uint64(12)
			if hdr[0] == 1 {
				entrySize = 20
			}
			if err := checkCountTable(hdr[4:8], payload-8, entrySize, b); err != nil {
				return err
			}
		case "stsz":
			hdr, err := readAt(r, b.payloadStart(), 12)
			if err != nil {
				return err
			}
			sampleSize := binary.BigEndian.Uint32(hdr[4:8])
			count := binary.BigEndian.Uint32(hdr[8:12])
			if count > MaxSamplesPerTrack && sampleSize == 0 {
				return clock.New(clock.StageScan, clock.CodeOversizedTable,
					"stsz declares %d samples", count).WithOffset(int64(b.Start))
			}
			if sampleSize == 0 {
				need := uint64(count) * 4
				if payload < 12 || payload-12 < need {
					return clock.New(clock.StageScan, clock.CodeTruncatedTable,
						"stsz declares %d sizes needing %d bytes but carries %d", count, need, payload-12).
						WithOffset(int64(b.Start))
				}
			}
		}
	}
	return nil
}

// checkFullTable validates a box whose payload starts with version/flags(4)
// then a 32-bit entry count.
func checkFullTable(r io.ReadSeeker, b rawBox, payload uint64, headerBytes, entrySize uint64, name string) error {
	if payload < headerBytes {
		return clock.New(clock.StageScan, clock.CodeTruncatedTable,
			"%s payload is %d bytes, header needs %d", name, payload, headerBytes).WithOffset(int64(b.Start))
	}
	buf, err := readAt(r, b.payloadStart(), 8)
	if err != nil {
		return err
	}
	return checkCountTable(buf[4:8], payload-8, entrySize, b)
}

func checkCountTable(countBytes []byte, available, entrySize uint64, b rawBox) error {
	count := binary.BigEndian.Uint32(countBytes)
	need := uint64(count) * entrySize
	if need > available {
		return clock.New(clock.StageScan, clock.CodeTruncatedTable,
			"%s declares %d entries needing %d bytes but carries %d", b.Type, count, need, available).
			WithOffset(int64(b.Start))
	}
	if count > MaxSamplesPerTrack {
		return clock.New(clock.StageScan, clock.CodeOversizedTable,
			"%s declares %d entries (limit %d)", b.Type, count, MaxSamplesPerTrack).
			WithOffset(int64(b.Start))
	}
	return nil
}

func readAt(r io.ReadSeeker, off uint64, n int) ([]byte, error) {
	if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
		return nil, clock.New(clock.StageScan, clock.CodeIO, "%v", err).WithOffset(int64(off))
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, clock.New(clock.StageScan, clock.CodeTruncatedTable, "read %d bytes at %d", n, off).WithOffset(int64(off))
	}
	return buf, nil
}

// findStbl locates every stbl box by walking moov/trak/mdia/minf with raw
// headers. It complements the typed scan with offset-accurate validation.
func findStbl(r io.ReadSeeker, size int64) ([]rawBox, error) {
	top, err := readBoxesFile(r, 0, uint64(size))
	if err != nil {
		return nil, err
	}
	var out []rawBox
	for _, b := range top {
		if b.Type != "moov" {
			continue
		}
		traks, err := readBoxesFile(r, b.payloadStart(), b.end())
		if err != nil {
			return nil, err
		}
		for _, t := range traks {
			if t.Type != "trak" {
				continue
			}
			// Read tkhd.track_ID so a failure inside this trak names the track.
			trackID, _ := readTrackID(r, t)
			mdias, err := readBoxesFile(r, t.payloadStart(), t.end())
			if err != nil {
				return nil, err
			}
			for _, md := range mdias {
				if md.Type != "mdia" {
					continue
				}
				minfs, err := readBoxesFile(r, md.payloadStart(), md.end())
				if err != nil {
					return nil, err
				}
				for _, mn := range minfs {
					if mn.Type != "minf" {
						continue
					}
					stbls, err := readBoxesFile(r, mn.payloadStart(), mn.end())
					if err != nil {
						return nil, err
					}
					for _, st := range stbls {
						if st.Type == "stbl" {
							st.TrackID = trackID
							out = append(out, st)
						}
					}
				}
			}
		}
	}
	return out, nil
}

// readTrackID parses tkhd just far enough to read track_ID. Layout: full box
// header (4) then, for version 0, creation(4) modification(4) track_ID(4); for
// version 1 the two times are 8 bytes each.
func readTrackID(r io.ReadSeeker, trak rawBox) (uint32, error) {
	children, err := readBoxesFile(r, trak.payloadStart(), trak.end())
	if err != nil {
		return 0, err
	}
	for _, c := range children {
		if c.Type != "tkhd" {
			continue
		}
		hdr, err := readAt(r, c.payloadStart(), 24)
		if err != nil {
			return 0, err
		}
		version := hdr[0]
		// version/flags(4); v0 creation(4) modification(4); v1 each 8 bytes.
		off := 12
		if version == 1 {
			off = 20
		}
		if off+4 > len(hdr) {
			return 0, nil
		}
		return binary.BigEndian.Uint32(hdr[off : off+4]), nil
	}
	return 0, nil
}
