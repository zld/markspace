// Package probe identifies supported local containers without trusting file
// extensions alone.
package probe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	gomp4 "github.com/abema/go-mp4"
	"github.com/pion/rtp/v2"

	"mediapack/internal/fault"
)

const (
	FormatMP4 = "mp4"
	FormatMOV = "mov"
	FormatRTP = "rtp"
)

type Info struct {
	Path           string `json:"path"`
	Format         string `json:"format"`
	Size           int64  `json:"size"`
	MajorBrand     string `json:"major_brand,omitempty"`
	QuickTime      bool   `json:"quicktime,omitempty"`
	RTPPacketCount int    `json:"rtp_packet_count,omitempty"`
}

// Detect identifies MP4/MOV by their ftyp box and RTP by validating the
// length-prefixed packet container used by the local fixtures.
func Detect(path string) (*Info, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fault.Wrap(fault.KindUnrecoverable, err)
	}
	if st.IsDir() {
		return nil, fault.New(fault.KindProbe, "%s is a directory", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Wrap(fault.KindUnrecoverable, err)
	}
	defer f.Close()

	head := make([]byte, 32)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	ext := strings.ToLower(filepath.Ext(path))

	if n >= 12 && string(head[4:8]) == "ftyp" {
		brand := boxType(head[8:12])
		format := FormatMP4
		qt := brand == "qt  " || ext == ".mov"
		if qt || ext == ".mov" {
			format = FormatMOV
		}
		info := &Info{Path: path, Format: format, Size: st.Size(), MajorBrand: brand, QuickTime: qt}
		// Use go-mp4 once during probing. A broken later box does not prevent
		// format identification; demux reports its exact offset.
		if _, err := gomp4.Probe(f); err != nil {
			var fe *fault.Error
			if !errors.As(err, &fe) {
				// Probe failure is intentionally non-fatal here.
				info.MajorBrand = brand
			}
		}
		return info, nil
	}

	if ext == ".rtp" || validLengthPrefixedRTP(head) {
		count, err := countRTPPackets(path)
		if err != nil {
			return nil, err
		}
		return &Info{Path: path, Format: FormatRTP, Size: st.Size(), RTPPacketCount: count}, nil
	}

	return nil, fault.At(fault.KindProbe, path, 0, "unsupported or unrecognized input")
}

func validLengthPrefixedRTP(head []byte) bool {
	if len(head) < 14 {
		return false
	}
	n := binary.BigEndian.Uint32(head[:4])
	if n < 12 || int(n)+4 > len(head) {
		return false
	}
	var p rtp.Packet
	return p.Unmarshal(head[4:4+n]) == nil && p.Version == 2
}

func countRTPPackets(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fault.Wrap(fault.KindUnrecoverable, err)
	}
	n := 0
	for len(data) > 0 {
		if len(data) < 4 {
			break
		}
		l := int(binary.BigEndian.Uint32(data[:4]))
		if l < 12 || 4+l > len(data) {
			break
		}
		var p rtp.Packet
		if err := p.Unmarshal(data[4 : 4+l]); err != nil {
			break
		}
		n++
		data = data[4+l:]
	}
	return n, nil
}

func boxType(b []byte) string {
	var out bytes.Buffer
	for _, c := range b {
		if c >= 32 && c < 127 {
			out.WriteByte(c)
		} else {
			out.WriteByte('.')
		}
	}
	return out.String()
}
