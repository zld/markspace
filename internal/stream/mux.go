package stream

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

// Mux envelope: [1-byte layer id][4-byte big-endian payload length][payload].
// The envelope lets one bounded reader carry several layers while preserving
// each layer's absolute byte offsets.
const muxHeaderLen = 5

// maxMuxRecord caps one envelope payload. Producers frame units no larger than
// their own chunk bound; this constant only rejects absurd or hostile lengths.
const maxMuxRecord = 64 << 20

// MuxSource interleaves routed items into one framed byte stream. It is an
// io.Reader so the bounded Reader owns the only buffering.
type MuxSource struct {
	feed    Feed
	layers  []string
	byName  map[string]byte
	pending []byte
	eof     bool
}

// NewMuxSource frames items from feed. Layer ids are assigned in sorted order
// so the framing is deterministic for a given layer set.
func NewMuxSource(feed Feed, layers []string) (*MuxSource, error) {
	if feed == nil {
		return nil, fmt.Errorf("stream: nil mux feed")
	}
	if len(layers) == 0 {
		return nil, fmt.Errorf("stream: mux needs at least one layer")
	}
	ids := make([]string, 0, len(layers))
	seen := map[string]bool{}
	for _, l := range layers {
		if l == "" || seen[l] {
			return nil, fmt.Errorf("stream: invalid or duplicate mux layer %q", l)
		}
		seen[l] = true
		ids = append(ids, l)
	}
	sort.Strings(ids)
	m := &MuxSource{feed: feed, layers: ids, byName: map[string]byte{}}
	for i, l := range ids {
		m.byName[l] = byte(i)
	}
	return m, nil
}

// Read implements io.Reader.
func (m *MuxSource) Read(p []byte) (int, error) {
	for len(m.pending) == 0 {
		if m.eof {
			return 0, io.EOF
		}
		item, err := m.feed.Next(context.Background())
		if err != nil {
			if err == io.EOF {
				m.eof = true
				return 0, io.EOF
			}
			return 0, err
		}
		id, ok := m.byName[item.Layer]
		if !ok {
			return 0, fmt.Errorf("stream: mux received unknown layer %q", item.Layer)
		}
		if len(item.Chunk.Data) > int(^uint32(0)) {
			return 0, fmt.Errorf("stream: mux payload too large")
		}
		rec := make([]byte, muxHeaderLen+len(item.Chunk.Data))
		rec[0] = id
		binary.BigEndian.PutUint32(rec[1:], uint32(len(item.Chunk.Data)))
		copy(rec[muxHeaderLen:], item.Chunk.Data)
		m.pending = rec
	}
	n := copy(p, m.pending)
	m.pending = m.pending[n:]
	return n, nil
}

// Demux incrementally decodes the framed stream into routed items. Its buffer
// is bounded by one record header plus one reader block, independent of the
// source length. Payload fragments are forwarded as soon as their bytes
// arrive, so a reader block boundary that splits a layer's payload is visible
// to the consumer exactly as it was read.
type Demux struct {
	src       *Reader
	byID      []string
	buf       []byte
	offset    map[string]int64
	maxRecord int
	err       error
}

// NewDemux builds a Feed over a bounded reader.
func NewDemux(src *Reader, layers []string) (*Demux, error) {
	if src == nil {
		return nil, fmt.Errorf("stream: nil demux reader")
	}
	maxRecord := maxMuxRecord
	ids := append([]string(nil), layers...)
	sort.Strings(ids)
	seen := map[string]bool{}
	for _, l := range ids {
		if l == "" || seen[l] {
			return nil, fmt.Errorf("stream: invalid or duplicate demux layer %q", l)
		}
		seen[l] = true
	}
	return &Demux{src: src, byID: ids, offset: map[string]int64{}, maxRecord: maxRecord}, nil
}

// Next implements Feed.
func (d *Demux) Next(ctx context.Context) (Item, error) {
	if d.err != nil {
		return Item{}, d.err
	}
	for {
		if item, ok := d.take(); ok {
			return item, nil
		}
		ch, err := d.src.Next(ctx)
		if err != nil {
			if err == io.EOF {
				if len(d.buf) != 0 {
					d.err = fmt.Errorf("stream: demux truncated mid-record (%d buffered bytes)", len(d.buf))
					return Item{}, d.err
				}
				return Item{}, io.EOF
			}
			d.err = err
			return Item{}, err
		}
		d.buf = append(d.buf, ch.Data...)
	}
}

// take decodes at most one complete record. Buffering is bounded by one
// record, which is at most one router chunk plus the envelope header.
func (d *Demux) take() (Item, bool) {
	if len(d.buf) < muxHeaderLen {
		return Item{}, false
	}
	id := int(d.buf[0])
	if id >= len(d.byID) {
		d.err = fmt.Errorf("stream: demux saw unknown layer id %d", id)
		return Item{}, false
	}
	length := int(binary.BigEndian.Uint32(d.buf[1:muxHeaderLen]))
	if length > d.maxRecord {
		d.err = fmt.Errorf("stream: demux record length %d exceeds bound %d", length, d.maxRecord)
		return Item{}, false
	}
	total := muxHeaderLen + length
	if len(d.buf) < total {
		return Item{}, false
	}
	layer := d.byID[id]
	payload := append([]byte(nil), d.buf[muxHeaderLen:total]...)
	d.buf = append(d.buf[:0], d.buf[total:]...)
	off := d.offset[layer]
	d.offset[layer] = off + int64(length)
	return Item{Layer: layer, Chunk: Chunk{Offset: off, Data: payload}}, true
}
