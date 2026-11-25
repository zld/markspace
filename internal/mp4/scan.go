package mp4

import (
	"encoding/binary"
	"io"
	"os"

	"mediacore/internal/clock"
)

var containerBoxes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "edts": true, "dinf": true, "udta": true,
	"mvex": true, "moof": true, "traf": true,
}

// ParseFile scans box boundaries without reading whole large media files.
// Sample tables are read only when their box is parsed by ParseFile.
func ParseFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.Wrap(clock.CategoryIO, path, 0, 0, "open mp4", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, clock.Wrap(clock.CategoryIO, path, 0, 0, "stat mp4", err)
	}
	root := &Box{Type: "root", Offset: 0, Size: st.Size(), PayloadOffset: 0}
	trackCount := 0
	if err := scanChildren(f, path, root, 0, st.Size(), &trackCount, 0); err != nil {
		return nil, err
	}
	file := &File{Path: path, root: root, fileSize: st.Size()}
	if err := file.parseMetadata(f); err != nil {
		return nil, err
	}
	return file, nil
}

func scanChildren(r io.ReadSeeker, path string, parent *Box, start, end int64, trackCount *int, inheritedTrack int) error {
	pos := start
	for pos < end {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return clock.Wrap(clock.CategoryIO, path, inheritedTrack, pos, "seek box header", err)
		}
		var header [16]byte
		n, err := io.ReadFull(r, header[:8])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return clock.New(clock.CategoryTruncatedBox, path, inheritedTrack, pos,
				"truncated box header")
		}
		if err != nil {
			return clock.Wrap(clock.CategoryIO, path, inheritedTrack, pos, "read box header", err)
		}
		if n < 8 {
			return clock.New(clock.CategoryTruncatedBox, path, inheritedTrack, pos,
				"short box header")
		}
		size32 := binary.BigEndian.Uint32(header[0:4])
		typ := string(header[4:8])
		headerSize := int64(8)
		size := int64(size32)
		if size32 == 1 {
			if _, err := io.ReadFull(r, header[8:16]); err != nil {
				return clock.New(clock.CategoryTruncatedBox, path, inheritedTrack, pos+8,
					"truncated largesize box header")
			}
			size = int64(binary.BigEndian.Uint64(header[8:16]))
			headerSize = 16
		} else if size32 == 0 {
			size = end - pos
		}
		if size < headerSize {
			return clock.New(clock.CategoryTruncatedBox, path, inheritedTrack, pos,
				"box size is smaller than header")
		}
		next := pos + size
		if next > end {
			return clock.New(clock.CategoryTruncatedBox, path, inheritedTrack, pos,
				"box extends past end of file or parent box")
		}
		track := inheritedTrack
		if typ == "trak" {
			*trackCount++
			track = *trackCount
		}
		box := &Box{
			Type: typ, Offset: pos, HeaderSize: headerSize,
			PayloadOffset: pos + headerSize, Size: size, Track: track,
		}
		parent.Children = append(parent.Children, box)
		if containerBoxes[typ] {
			childTrackCount := 0
			if typ == "trak" {
				// Track numbering is assigned by this box; descendants inherit it.
				childTrackCount = 0
			}
			if err := scanChildren(r, path, box, box.PayloadOffset, next, &childTrackCount, track); err != nil {
				return err
			}
		}
		pos = next
	}
	return nil
}

type readAtSeeker interface {
	io.ReaderAt
	io.ReadSeeker
}

func readBoxPayload(r readAtSeeker, path string, b *Box, track int) ([]byte, error) {
	size := b.Size - b.HeaderSize
	if size < 0 || size > maxBoxPayload {
		return nil, clock.New(clock.CategoryInvalidBox, path, track, b.Offset, "invalid payload size")
	}
	buf := make([]byte, size)
	if _, err := r.ReadAt(buf, b.PayloadOffset); err != nil {
		return nil, clock.Wrap(clock.CategoryTruncatedBox, path, track, b.Offset, "read box payload", err)
	}
	return buf, nil
}

const maxBoxPayload = 256 << 20
