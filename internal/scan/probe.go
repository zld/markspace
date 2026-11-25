package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediacore/internal/fault"
)

// Format identifies a supported input family.
type Format string

const (
	FormatUnknown Format = "unknown"
	FormatMP4     Format = "mp4"
	FormatMOV     Format = "mov"
	FormatRTP     Format = "rtp-fixture"
)

// RTPMagic identifies the generated length-prefixed RTP fixture format.
const RTPMagic = "MCRTPIDX1\n"

// SourceInfo is the result of cheap format probing.
type SourceInfo struct {
	Path   string `json:"path"`
	Format Format `json:"format"`
	Size   int64  `json:"size"`
}

// Probe identifies MP4/MOV boxes or the local RTP fixture container.
func Probe(path string) (*SourceInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fault.At(fault.Wrap(fault.NotFound, err), path, -1)
	}
	if st.IsDir() {
		return nil, fault.New(fault.InvalidArgument, "input is a directory: "+path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	defer f.Close()

	head := make([]byte, 32)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	head = head[:n]

	info := &SourceInfo{Path: path, Size: st.Size()}
	switch {
	case n >= len(RTPMagic) && string(head[:len(RTPMagic)]) == RTPMagic:
		info.Format = FormatRTP
	case n >= 12 && string(head[4:8]) == "ftyp":
		if string(head[8:12]) == "qt  " {
			info.Format = FormatMOV
		} else {
			info.Format = FormatMP4
		}
	default:
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".mp4" || ext == ".m4v" {
			info.Format = FormatMP4
		} else if ext == ".mov" {
			info.Format = FormatMOV
		} else if ext == ".rtpbin" {
			info.Format = FormatRTP
		} else {
			return nil, fault.New(fault.UnsupportedFormat, "cannot identify input: "+path)
		}
	}
	return info, nil
}

// SHA256File returns the hex SHA-256 digest without loading the whole file.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, 1<<40)); err != nil {
		return "", fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
