package probe

import (
	"bytes"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"archive/internal/clock"
)

// Sniff determines a container format from its leading bytes and probes it.
func Sniff(data []byte) (*Container, error) {
	if len(data) >= 8 && string(data[4:8]) == "ftyp" {
		return ProbeMP4(bytes.NewReader(data))
	}
	if bytes.Contains(data, []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05}) {
		return ProbeMXF(data)
	}
	// Fall back to the MP4 reader so the error carries a structural offset.
	if _, err := mp4.ReadBoxInfo(bytes.NewReader(data)); err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil, clock.New(clock.KindTruncated, "file shorter than a container header").
			WithLocation(fmt.Sprintf("offset 0 (size %d)", len(data)))
	}
	return nil, clock.New(clock.KindInvalidMetadata, "unrecognised container format").
		WithLocation("offset 0")
}
