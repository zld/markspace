// Package tsreader reads 188-byte MPEG-TS packets in file order and
// preserves the packet index so downstream stages can attribute every
// packet to a mapping interval. Packet decoding is delegated to
// go-astits; this package only flattens what the pipeline needs.
package tsreader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/asticode/go-astits"
)

// Packet is a decoded TS packet with its position in the input.
type Packet struct {
	Index          int    // 0-based packet index in input order
	PID            uint16 // packet identifier
	CC             uint8  // continuity counter
	PUSI           bool   // payload unit start indicator
	TransportError bool   // transport error indicator
	HasPayload     bool   // packet carries payload (vs adaptation-only)
	Payload        []byte // payload after the adaptation field (nil if none)
}

// Read streams packets from r in input order, invoking fn for each.
// Reading stops at the first incomplete tail or read error; a clean
// end of stream returns nil.
func Read(r io.Reader, fn func(Packet) error) error {
	dmx := astits.NewDemuxer(context.Background(), r)
	idx := 0
	for {
		p, err := dmx.NextPacket()
		if err != nil {
			if errors.Is(err, astits.ErrNoMorePackets) {
				return nil
			}
			return fmt.Errorf("reading packet %d: %w", idx, err)
		}
		pkt := Packet{
			Index:          idx,
			PID:            p.Header.PID,
			CC:             p.Header.ContinuityCounter,
			PUSI:           p.Header.PayloadUnitStartIndicator,
			TransportError: p.Header.TransportErrorIndicator,
			HasPayload:     p.Header.HasPayload,
			Payload:        p.Payload,
		}
		idx++
		if err := fn(pkt); err != nil {
			return err
		}
	}
}

// ReadFile opens path and streams its packets through Read.
func ReadFile(path string, fn func(Packet) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening input: %w", err)
	}
	defer f.Close()
	return Read(f, fn)
}
