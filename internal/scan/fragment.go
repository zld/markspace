// Package scan turns generated fMP4 fragments into validated fragment
// records. It verifies ISO BMFF box boundaries with go-mp4, extracts mfhd
// sequence numbers and tfdt decode times, and parses the RTP transmission
// records that describe cross-node arrival boundaries.
//
// A truncated or malformed box is classified as fault.CodeTruncatedBox and
// never produces a fragment record, so it cannot enter the final manifest.
package scan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"mediapipeline/internal/fault"
)

// Fragment is one validated input fragment in input-index order.
type Fragment struct {
	// InputIndex is the zero-based arrival index within the representation.
	InputIndex int
	// FileName is the base name of the source fragment file.
	FileName string
	// InputPath is the repository-relative source path (rep0/frag-003.m4s).
	InputPath string
	// SHA256 is the hex-encoded digest of the complete file bytes.
	SHA256 string
	// Size is the file size in bytes.
	Size int64
	// MFHD is the Movie Fragment Sequence Number.
	MFHD uint32
	// TFDT is the BaseMediaDecodeTime in track timescale ticks.
	TFDT uint64
}

// InitInfo describes a parsed init segment.
type InitInfo struct {
	TrackID   uint32
	Timescale uint32
	SHA256    string
}

// ReadInitSegment parses an init.mp4 and extracts the track timescale.
func ReadInitSegment(path string) (*InitInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info := &InitInfo{SHA256: digest(data)}
	r := bytes.NewReader(data)
	_, err = mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMdhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if mdhd, ok := box.(*mp4.Mdhd); ok && info.Timescale == 0 {
				info.Timescale = mdhd.Timescale
			}
		case mp4.BoxTypeTkhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if tkhd, ok := box.(*mp4.Tkhd); ok && info.TrackID == 0 {
				info.TrackID = tkhd.TrackID
			}
		}
		if h.BoxInfo.Type == mp4.BoxTypeMdhd() || h.BoxInfo.Type == mp4.BoxTypeTkhd() {
			return nil, nil
		}
		if _, err := h.Expand(); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return nil, fault.New(fault.CodeTruncatedBox, "init segment unreadable: %v", err).WithInput(path)
	}
	if info.Timescale == 0 {
		return nil, fault.New(fault.CodeTruncatedBox, "mdhd timescale missing").WithInput(path)
	}
	return info, nil
}

// ReadFragment validates one fragment file and converts it to a record.
func ReadFragment(inputIndex int, repID, fullPath string) (*Fragment, error) {
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}
	frag := &Fragment{
		InputIndex: inputIndex,
		FileName:   filepath.Base(fullPath),
		InputPath:  filepath.Join(repID, filepath.Base(fullPath)),
		SHA256:     digest(data),
		Size:       int64(len(data)),
	}
	r := bytes.NewReader(data)
	totalSize := int64(len(data))
	haveStyp, haveMoof, haveMdat := false, false, false
	for {
		bi, rerr := mp4.ReadBoxInfo(r)
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, truncated(fullPath, "box header unreadable: %v", rerr)
		}
		if bi.Offset+bi.Size > uint64(totalSize) {
			return nil, truncated(fullPath, "box %s declares size %d ending past EOF",
				bi.Type.String(), bi.Size)
		}
		switch bi.Type {
		case mp4.BoxTypeStyp(), mp4.BoxTypeFtyp():
			haveStyp = true
		case mp4.BoxTypeMoof():
			haveMoof = true
			if perr := parseMoof(r, bi, frag); perr != nil {
				return nil, perr
			}
		case mp4.BoxTypeMdat():
			haveMdat = true
		}
		if _, serr := bi.SeekToEnd(r); serr != nil {
			return nil, truncated(fullPath, "cannot seek past box %s: %v", bi.Type.String(), serr)
		}
	}
	if !haveStyp || !haveMoof || !haveMdat {
		return nil, truncated(fullPath, "missing required box (styp=%v moof=%v mdat=%v)",
			haveStyp, haveMoof, haveMdat)
	}
	return frag, nil
}

// parseMoof walks the moof container and extracts mfhd and tfdt values,
// checking every nested box against the fragment's byte boundaries.
func parseMoof(r io.ReadSeeker, moof *mp4.BoxInfo, frag *Fragment) error {
	_, err := mp4.ReadBoxStructureFromInternal(r, moof, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMfhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			frag.MFHD = box.(*mp4.Mfhd).SequenceNumber
		case mp4.BoxTypeTfdt():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			frag.TFDT = box.(*mp4.Tfdt).GetBaseMediaDecodeTime()
		}
		if _, err := h.Expand(); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return truncated(frag.InputPath, "moof payload unreadable: %v", err)
	}
	if frag.MFHD == 0 {
		return truncated(frag.InputPath, "mfhd sequence number missing")
	}
	return nil
}

func truncated(input string, format string, args ...any) *fault.Error {
	return fault.New(fault.CodeTruncatedBox, fmt.Sprintf(format, args...)).WithInput(input)
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
