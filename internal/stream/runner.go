// Package stream executes a scanned index record by record.
//
// Execution is deliberately "confirmed records first": every sample that is
// fully backed by the container and, for audio, by a delivered packet is
// committed before the next one is examined. When a record fails, the run
// stops at that record, keeps everything already confirmed, and reports the
// first failure position. Failed records never enter the success counters.
package stream

import (
	"fmt"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/index"
)

// Record is one executed sample.
type Record struct {
	TrackID uint32
	Kind    string
	Sample  int
	Offset  int64
	DTS     clock.Nanos
	End     clock.Nanos
	Bytes   uint32
	Packet  uint64
	HasPkt  bool
	Time    clock.Nanos
	ByteDev int64
	TimeDev clock.Nanos
	OK      bool
	Note    string
}

// Outcome is the streaming result.
type Outcome struct {
	Records   []Record
	Faults    []*fault.Error
	Confirmed int
	Failed    int
	Skipped   int
	FirstFail *fault.Error
	ByteDev   int64
	TimeDev   clock.Nanos
	Completed bool
}

// Run executes idx. mp4 sizes are re-checked against the input bytes so a
// truncated input cannot be reported as confirmed.
func Run(idx *index.Index, mp4 []byte) *Outcome {
	out := &Outcome{TimeDev: 0}
	if idx == nil {
		out.FirstFail = fault.New(fault.StageStream, "stream.no_index", fault.NoOffset, clock.Invalid, "no index to stream")
		out.Faults = append(out.Faults, out.FirstFail)
		return out
	}
	if !idx.Complete {
		// A partial scan still streams its confirmed container records; the
		// earliest scan fault becomes this run's first failure.
		if len(idx.Faults) > 0 {
			out.fail(idx.Faults[0])
		} else {
			out.fail(fault.New(fault.StageStream, "stream.index_incomplete", fault.NoOffset, clock.Invalid,
				"index did not complete its scan"))
		}
	}
	packets := packetIndex(idx)
	audio := idx.AudioTrack()
	size := int64(len(mp4))
	// Interleave tracks in decode-time order so the stream follows the
	// asset's presentation rather than track order.
	for _, ref := range order(idx) {
		tr := idx.TrackByID(ref.track)
		if tr == nil {
			continue
		}
		s := &tr.Samples[ref.sample]
		rec := Record{
			TrackID: tr.ID,
			Kind:    tr.Kind,
			Sample:  ref.sample,
			Offset:  s.Offset,
			DTS:     s.DTS,
			End:     clock.Add(s.DTS, s.Duration),
			Bytes:   s.Size,
			Time:    clock.Invalid,
		}
		if s.Offset < 0 || s.Offset+int64(s.Size) > size {
			out.fail(fault.New(fault.StageStream, "stream.sample.truncated", s.Offset, s.DTS,
				fmt.Sprintf("track %d sample %d needs bytes %d..%d but input ends at %d",
					tr.ID, ref.sample, s.Offset, s.Offset+int64(s.Size), size)).WithSample(ref.sample))
			continue
		}
		if tr.Kind == "audio" && audio != nil && tr.ID == audio.ID {
			p, ok := packets[s.DTS]
			if !ok {
				rec.Note = "no delivered packet"
				out.fail(fault.New(fault.StageStream, "stream.packet.missing", s.Offset, s.DTS,
					fmt.Sprintf("audio sample %d at %v has no delivered packet", ref.sample, s.DTS)).WithSample(ref.sample))
				continue
			}
			rec.Packet = p.SeqExt
			rec.HasPkt = true
			rec.Time = p.Time
			// Both sides describe the same payload, so size is the
			// comparable byte quantity; stream positions live in different
			// files and must not be subtracted.
			rec.ByteDev = int64(p.PayloadSize) - int64(s.Size)
			rec.TimeDev = clock.Sub(p.Time, s.DTS)
			out.ByteDev += rec.ByteDev
			out.TimeDev = clock.Add(out.TimeDev, rec.TimeDev)
			if rec.ByteDev != 0 || rec.TimeDev != 0 {
				out.fail(fault.New(fault.StageStream, "stream.sample.deviation", s.Offset, s.DTS,
					fmt.Sprintf("audio sample %d deviates by %d byte(s) and %v", ref.sample, rec.ByteDev, rec.TimeDev)).
					WithSample(ref.sample))
				continue
			}
		}
		rec.OK = true
		out.Records = append(out.Records, rec)
		out.Confirmed++
	}
	if out.FirstFail != nil {
		if skipped := totalSamples(idx) - out.Confirmed - out.Failed; skipped > 0 {
			out.Skipped = skipped
		}
	}
	out.Completed = out.FirstFail == nil
	fault.Sort(out.Faults)
	return out
}

func (o *Outcome) fail(err *fault.Error) {
	if o.FirstFail == nil {
		o.FirstFail = err
	}
	o.Failed++
	o.Faults = append(o.Faults, err)
}

// sampleRef identifies one sample of one track.
type sampleRef struct {
	track  uint32
	sample int
}

// order returns every sample reference sorted by decode time, then track,
// then sample index. The ordering is stable so repeated runs agree.
func order(idx *index.Index) []sampleRef {
	refs := make([]sampleRef, 0, totalSamples(idx))
	for ti := range idx.Tracks {
		for si := range idx.Tracks[ti].Samples {
			refs = append(refs, sampleRef{track: idx.Tracks[ti].ID, sample: si})
		}
	}
	// Insertion sort keeps this dependency-free; index sizes are bounded by
	// the memory budget documented in the README.
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && less(idx, refs[j], refs[j-1]); j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}
	return refs
}

func less(idx *index.Index, a, b sampleRef) bool {
	ta, tb := idx.TrackByID(a.track), idx.TrackByID(b.track)
	da, db := ta.Samples[a.sample].DTS, tb.Samples[b.sample].DTS
	if da != db {
		return da < db
	}
	if a.track != b.track {
		return a.track < b.track
	}
	return a.sample < b.sample
}

func totalSamples(idx *index.Index) int {
	n := 0
	for i := range idx.Tracks {
		n += len(idx.Tracks[i].Samples)
	}
	return n
}

// packetIndex maps the media time of each deliverable packet to it. A gap
// record occupies no media time and is never a valid match.
func packetIndex(idx *index.Index) map[clock.Nanos]*index.Packet {
	m := make(map[clock.Nanos]*index.Packet, len(idx.Packets))
	for i := range idx.Packets {
		p := &idx.Packets[i]
		if p.Kind == index.PacketOK {
			if _, exists := m[p.Time]; !exists {
				m[p.Time] = p
			}
		}
	}
	return m
}
