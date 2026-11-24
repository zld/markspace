// Package stream is phase two of the pipeline: it walks the scan result in
// a deterministic (PTS, track) order, hands every intact sample to the RTP
// jitter buffer of its track, and yields the recovered packet stream.
// Corrupt samples are skipped (counted), never repaired by guessing.
package stream

import (
	"errors"
	"io"
	"os"
	"sort"

	pionrtp "github.com/pion/rtp"

	"archive/internal/model"
	"archive/internal/scan"
	"archive/internal/rtp"
)

// Packet is one recovered media packet on the master timeline.
type Packet struct {
	TrackID int
	Type    string
	Seq     uint16
	PtsNs   int64
	Payload []byte // essence packet, CRC-validated at scan time
}

// Stats counts what the executor did with the input packets.
type Stats struct {
	Recovered int // packets emitted to the output
	Skipped   int // samples dropped because scan marked them bad
	Lost      int // sequence gaps the jitter window gave up on
}

type item struct {
	trackIdx int
	seq      uint16
	ptsNs    int64
	sample   scan.SampleRef
}

// Demuxer merges all tracks of a scan result into one ordered stream.
type Demuxer struct {
	f       *os.File
	tracks  []scan.TrackMeta
	items   []item
	pos     int
	bufs    []*rtp.Buffer
	queue   []Packet
	flushed bool
	stats   Stats
	Errs    model.Errors
}

// NewDemuxer prepares the merged stream. Samples marked bad by the scanner
// are counted as skipped up front; their sequence numbers stay reserved so
// the jitter buffer sees the gap.
func NewDemuxer(res *scan.Result, f *os.File, cfg rtp.Config) *Demuxer {
	d := &Demuxer{f: f, tracks: res.Tracks}
	for ti := range res.Tracks {
		t := &res.Tracks[ti]
		d.bufs = append(d.bufs, rtp.NewBuffer(cfg))
		for si, smp := range t.Samples {
			if smp.Bad {
				d.stats.Skipped++
				continue
			}
			d.items = append(d.items, item{
				trackIdx: ti,
				seq:      uint16(si),
				ptsNs:    model.ToMaster(smp.PTS, model.Timescale(t.Timescale)),
				sample:   smp,
			})
		}
	}
	sort.SliceStable(d.items, func(i, j int) bool {
		if d.items[i].ptsNs != d.items[j].ptsNs {
			return d.items[i].ptsNs < d.items[j].ptsNs
		}
		return d.items[i].trackIdx < d.items[j].trackIdx
	})
	return d
}

// Stats returns a copy of the current counters.
func (d *Demuxer) Stats() Stats { return d.stats }

// Next returns the next recovered packet, or io.EOF at end of stream.
func (d *Demuxer) Next() (*Packet, error) {
	for len(d.queue) == 0 {
		if d.pos < len(d.items) {
			d.feed(d.items[d.pos])
			d.pos++
			continue
		}
		if !d.flushed {
			d.flushed = true
			for ti := range d.bufs {
				d.drain(ti, d.bufs[ti].Flush())
			}
			continue
		}
		return nil, io.EOF
	}
	p := d.queue[0]
	d.queue = d.queue[1:]
	return &p, nil
}

// feed reads one sample, round-trips it through the RTP transport format
// and pushes it into its track's jitter buffer.
func (d *Demuxer) feed(it item) {
	t := &d.tracks[it.trackIdx]
	raw := make([]byte, it.sample.Size)
	if _, err := d.f.ReadAt(raw, it.sample.Offset); err != nil && !errors.Is(err, io.EOF) {
		d.stats.Skipped++
		return
	}
	pkt := pionrtp.Packet{
		Header: pionrtp.Header{
			Version:        2,
			PayloadType:    payloadType(t.Type),
			SequenceNumber: it.seq,
			Timestamp:      model.ToRTP(it.ptsNs),
			SSRC:           uint32(t.ID),
		},
		Payload: raw,
	}
	wire, err := pkt.Marshal()
	if err != nil {
		d.stats.Skipped++
		return
	}
	var back pionrtp.Packet
	if err := back.Unmarshal(wire); err != nil {
		d.stats.Skipped++
		return
	}
	if jump := d.bufs[it.trackIdx].Push(&back); jump {
		d.Errs = append(d.Errs, model.ScanError{Code: model.ErrTimestampJump,
			Offset: it.sample.Offset, Track: t.ID, Detail: "RTP timestamp discontinuity"})
	}
	d.drain(it.trackIdx, nil)
}

// drain pulls every ready event out of one track's buffer.
func (d *Demuxer) drain(ti int, extra []rtp.Event) {
	push := func(ev rtp.Event) {
		if len(ev.Lost) > 0 {
			d.stats.Lost += len(ev.Lost)
			d.Errs = append(d.Errs, model.ScanError{Code: model.ErrPacketLost,
				Track: d.tracks[ti].ID, Detail: "sequence gap expired the jitter window"})
		}
		if ev.Packet != nil {
			d.queue = append(d.queue, Packet{
				TrackID: d.tracks[ti].ID,
				Type:    d.tracks[ti].Type,
				Seq:     ev.Packet.SequenceNumber,
				PtsNs:   model.FromRTP(ev.Packet.Timestamp),
				Payload: ev.Packet.Payload,
			})
			d.stats.Recovered++
		}
	}
	for {
		ev := d.bufs[ti].Next()
		if ev == nil {
			break
		}
		push(*ev)
	}
	for _, ev := range extra {
		push(ev)
	}
}

func payloadType(trackType string) uint8 {
	if trackType == "video" {
		return 96
	}
	return 97
}
