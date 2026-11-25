// Package stream parses an ISO-BMFF sample table into a timeline of coded
// samples, identifying sync (key) frames and validating that every sample
// the table declares is actually present in the file.
//
// The parser accepts progressive MP4 (moov with stbl). It does not decode
// payloads; only byte ranges and timing are needed for clip cutting. When a
// declared sample payload is missing, the first such offset is reported and
// later samples are still described, so a truncated download can be
// partially cut.
package stream

import (
	"errors"
	"io"

	"github.com/abema/go-mp4"

	"replaycheck/internal/clock"
	"replaycheck/internal/fault"
)

// Sample is one coded unit.
type Sample struct {
	Index    int
	Offset   int64
	Size     int64
	Tick     int64 // decode time in track ticks
	Duration int64 // track ticks
	Key      bool
}

// Track is one parsed media track.
type Track struct {
	ID        uint32
	Timescale uint32
	Duration  int64
	Samples   []Sample
	Width     int
	Height    int
}

// Info is the completed parse result.
type Info struct {
	File      string
	Timescale uint32
	Duration  int64
	Track     *Track
	Issues    fault.List
	// FirstBadOffset is the offset of the first sample whose payload is not
	// fully present, or -1.
	FirstBadOffset int64
	// FirstBadTick is the decode time of that sample, or -1.
	FirstBadTick int64
	// KeyframeTicks are the decode times of sync samples.
	KeyframeTicks []int64
	// KeyframeIntervals are tick deltas between consecutive keyframes.
	KeyframeIntervals []int64
}

// Timeline returns a timeline bound to the track timescale and the given
// PCM sample rate.
func (i *Info) Timeline(sampleRate int) (*clock.Timeline, error) {
	r, err := clock.NewRate(int64(i.Timescale), 1)
	if err != nil {
		return nil, err
	}
	return clock.New(r, sampleRate)
}

// KeyframeAtOrBefore returns the index of the latest sync sample whose tick
// is <= tick. The boolean is false when no such sample exists.
func (t *Track) KeyframeAtOrBefore(tick int64) (int, bool) {
	idx := -1
	for i := range t.Samples {
		if t.Samples[i].Tick > tick {
			break
		}
		if t.Samples[i].Key {
			idx = i
		}
	}
	return idx, idx >= 0
}

// ParseFile parses a progressive MP4. size is the file's byte length.
func ParseFile(r io.ReadSeeker, file string, size int64) (*Info, error) {
	info := &Info{File: file, FirstBadOffset: -1, FirstBadTick: -1}
	probe, err := mp4.Probe(r)
	if err != nil {
		return nil, fault.Wrap(fault.KindUnreadable, fault.Fatal, fault.ByteAt(0), err,
			"mp4 structure unreadable")
	}
	if probe.Timescale == 0 {
		return nil, fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(0),
			"zero movie timescale")
	}
	info.Timescale = probe.Timescale
	info.Duration = int64(probe.Duration)
	if len(probe.Tracks) == 0 {
		return nil, fault.New(fault.KindUnreadable, fault.Fatal, fault.ByteAt(0),
			"no media tracks in file")
	}

	track := probe.Tracks[0]
	if track.Timescale == 0 {
		return nil, fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(0),
			"zero track timescale")
	}
	tr := &Track{ID: track.TrackID, Timescale: track.Timescale, Duration: int64(track.Duration)}
	if track.AVC != nil {
		tr.Width, tr.Height = int(track.AVC.Width), int(track.AVC.Height)
	}
	info.Track = tr

	syncSet, err := syncSamples(r, track.TrackID)
	if err != nil {
		if !errors.Is(err, errNoStss) {
			info.Issues.Add(fault.Wrap(fault.KindUnreadable, fault.Damaged, fault.ByteAt(0), err,
				"sync sample table unreadable; treating every sample as a sync sample"))
		}
		syncSet = nil // no stss: every sample is a sync sample
	}

	var decode int64
	for si, s := range track.Samples {
		tr.Samples = append(tr.Samples, Sample{
			Index:    si,
			Size:     int64(s.Size),
			Tick:     decode,
			Duration: int64(s.TimeDelta),
			Key:      isSync(syncSet, si),
		})
		decode += int64(s.TimeDelta)
	}

	if len(track.Chunks) > 0 {
		assignChunkOffsets(tr, track.Chunks, info)
	} else {
		var off int64
		for i := range tr.Samples {
			tr.Samples[i].Offset = off
			off += tr.Samples[i].Size
		}
	}

	for _, sm := range tr.Samples {
		if sm.Offset+sm.Size > size {
			if info.FirstBadOffset < 0 || sm.Offset < info.FirstBadOffset {
				info.FirstBadOffset = sm.Offset
				info.FirstBadTick = sm.Tick
			}
			ms := clock.TicksToMillisSafe(sm.Tick, int64(track.Timescale))
			info.Issues.Add(fault.New(fault.KindUnreadable, fault.Damaged,
				fault.Location{ByteOffset: sm.Offset, Tick: sm.Tick, HasTick: true, Millis: ms, Label: "sample"},
				"sample %d claims %d bytes at %d; file is %d bytes",
				sm.Index, sm.Size, sm.Offset, size))
			break
		}
	}

	for _, sm := range tr.Samples {
		if sm.Key {
			info.KeyframeTicks = append(info.KeyframeTicks, sm.Tick)
		}
	}
	for i := 1; i < len(info.KeyframeTicks); i++ {
		info.KeyframeIntervals = append(info.KeyframeIntervals,
			info.KeyframeTicks[i]-info.KeyframeTicks[i-1])
	}
	if len(info.KeyframeTicks) == 0 {
		info.Issues.Add(fault.New(fault.KindMissingKeyframe, fault.Damaged, fault.ByteAt(0),
			"no sync samples declared"))
	}
	return info, nil
}

// assignChunkOffsets maps the stco/stsc chunk table onto the sample list.
// Each chunk lists SamplesPerChunk consecutive samples whose sizes are taken
// from stsz, so the first sample of a chunk starts at the chunk offset.
func assignChunkOffsets(tr *Track, chunks []*mp4.Chunk, info *Info) {
	si := 0
	for _, ch := range chunks {
		if si >= len(tr.Samples) {
			break
		}
		cur := int64(ch.DataOffset)
		n := int(ch.SamplesPerChunk)
		if n == 0 {
			info.Issues.Add(fault.New(fault.KindBadChunk, fault.Damaged,
				fault.ByteAt(cur), "chunk at %d declares zero samples", cur))
			continue
		}
		for k := 0; k < n && si < len(tr.Samples); k++ {
			tr.Samples[si].Offset = cur
			cur += tr.Samples[si].Size
			si++
		}
	}
	if si != len(tr.Samples) {
		info.Issues.Add(fault.New(fault.KindBadChunk, fault.Damaged,
			fault.ByteAt(int64(si)), "chunk table covers %d of %d samples", si, len(tr.Samples)))
	}
}

var errNoStss = errors.New("stss not present")

func syncSamples(r io.ReadSeeker, trackID uint32) (map[int]bool, error) {
	moovs, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{{mp4.BoxTypeMoov()}})
	if err != nil || len(moovs) == 0 {
		return nil, err
	}
	traks, err := mp4.ExtractBoxes(r, moovs[0], []mp4.BoxPath{{mp4.BoxTypeTrak()}})
	if err != nil {
		return nil, err
	}
	for _, trak := range traks {
		id, err := trakID(r, trak)
		if err != nil {
			continue
		}
		if id != trackID {
			continue
		}
		stss, err := mp4.ExtractBoxesWithPayload(r, trak, []mp4.BoxPath{
			{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()},
		})
		if err != nil {
			return nil, err
		}
		if len(stss) == 0 {
			return nil, errNoStss
		}
		m := make(map[int]bool)
		for _, n := range stss[0].Payload.(*mp4.Stss).SampleNumber {
			m[int(n)-1] = true // stss sample numbers are 1-based
		}
		return m, nil
	}
	return nil, errNoStss
}

func trakID(r io.ReadSeeker, trak *mp4.BoxInfo) (uint32, error) {
	tkhds, err := mp4.ExtractBoxesWithPayload(r, trak, []mp4.BoxPath{{mp4.BoxTypeTkhd()}})
	if err != nil {
		return 0, err
	}
	if len(tkhds) == 0 {
		return 0, errNoStss
	}
	return tkhds[0].Payload.(*mp4.Tkhd).TrackID, nil
}

func isSync(m map[int]bool, idx int) bool {
	if m == nil {
		return true
	}
	return m[idx]
}
