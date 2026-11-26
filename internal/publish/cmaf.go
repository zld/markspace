// Package publish writes CMAF/fMP4 initialization and media segments, the
// delivery manifest and the RTP time-audit trail. Products are assembled in a
// staging directory and promoted atomically after hashing; the independent
// verifier in internal/verify re-parses every artifact rather than reusing
// the planner's accept decision.
package publish

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
)

// BoxType aliases re-exported for readability in the split logic.
func typeFTYP() mp4.BoxType { return mp4.BoxTypeFtyp() }
func typeMOOV() mp4.BoxType { return mp4.BoxTypeMoov() }
func typeMOOF() mp4.BoxType { return mp4.BoxTypeMoof() }
func typeMDAT() mp4.BoxType { return mp4.BoxTypeMdat() }
func typeSTYP() mp4.BoxType { return mp4.BoxTypeStyp() }

// topLevelBox reads one MP4 box header, returning its type, total size and the
// absolute payload offset within the reader.
func topLevelBox(r io.ReadSeeker) (typ mp4.BoxType, size int64, payloadOff int64, err error) {
	var hdr [8]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return mp4.BoxType{}, 0, 0, err
	}
	size = int64(binary.BigEndian.Uint32(hdr[0:4]))
	copy(typ[:], hdr[4:8])
	hdrSize := int64(8)
	if size == 1 {
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return typ, 0, 0, err
		}
		size = int64(binary.BigEndian.Uint64(ext[:]))
		hdrSize = 16
	}
	if size < hdrSize {
		return typ, 0, 0, fmt.Errorf("invalid box size %d", size)
	}
	off, _ := r.Seek(0, io.SeekCurrent)
	return typ, size, off, nil
}

// SplitFragmentedMP4 decomposes a single fragmented MP4 (ftyp+moov then a
// sequence of moof/mdat pairs) into an init segment (ftyp+moov) and one media
// segment per fragment. Each media segment is prefixed with an styp box.
func SplitFragmentedMP4(src io.ReadSeeker, outDir, baseName string) (initPath string, mediaPaths []string, err error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", nil, err
	}
	var initBuf bytes.Buffer
	segIndex := 0
	flushSegment := func(frag bytes.Buffer) error {
		if frag.Len() == 0 {
			return nil
		}
		path := filepath.Join(outDir, fmt.Sprintf("%s-%d.m4s", baseName, segIndex))
		styp := stypBox()
		if err := os.WriteFile(path, append(styp, frag.Bytes()...), 0o644); err != nil {
			return err
		}
		mediaPaths = append(mediaPaths, path)
		segIndex++
		return nil
	}

	var frag bytes.Buffer
	for {
		start, _ := src.Seek(0, io.SeekCurrent)
		typ, size, _, herr := topLevelBox(src)
		if errors.Is(herr, io.EOF) || errors.Is(herr, io.ErrUnexpectedEOF) {
			break
		}
		if herr != nil {
			return "", nil, herr
		}
		// Capture the full box bytes.
		if _, err := src.Seek(start, io.SeekStart); err != nil {
			return "", nil, err
		}
		box := make([]byte, size)
		if _, err := io.ReadFull(src, box); err != nil {
			return "", nil, err
		}
		switch typ {
		case typeFTYP(), typeMOOV():
			initBuf.Write(box)
		case typeMOOF():
			if err := flushSegment(frag); err != nil {
				return "", nil, err
			}
			frag.Reset()
			frag.Write(box)
		case typeMDAT():
			if frag.Len() == 0 {
				return "", nil, errors.New("mdat without preceding moof")
			}
			frag.Write(box)
		default:
			// Keep any other top-level box attached to the current fragment.
			if frag.Len() > 0 {
				frag.Write(box)
			}
		}
	}
	if err := flushSegment(frag); err != nil {
		return "", nil, err
	}
	initPath = filepath.Join(outDir, baseName+"-init.mp4")
	if err := os.WriteFile(initPath, initBuf.Bytes(), 0o644); err != nil {
		return "", nil, err
	}
	return initPath, mediaPaths, nil
}

// stypBox builds the CMAF segment-type box (major brand msdh, compatible
// brands msdh/msix/iso6) with an explicit 8-byte header.
func stypBox() []byte {
	major := []byte("msdh")
	compat := []byte("msdhmsixiso6")
	body := append(append([]byte{}, major...), 0, 0, 0, 0) // minor version 0
	body = append(body, compat...)
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(body)))
	copy(out[4:8], "styp")
	return append(out, body...)
}
