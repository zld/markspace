// Package demux turns the probed container metadata into a stream of packets
// at the original container boundaries. It never re-encodes or repacks: the
// payload bytes handed to the remux stage are exactly the bytes found in the
// input, and the timestamps are the ones the container declared.
package demux

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"archive/internal/essence"
	"archive/internal/fault"
	"archive/internal/model"
	"archive/internal/probe"
)

// Packet is one container-boundary payload with its original timing.
type Packet struct {
	TrackKey  string         `json:"track_key"`
	TrackID   uint32         `json:"track_id"`
	Seq       int            `json:"seq"`
	StartTick uint64         `json:"start_tick"`
	Duration  uint64         `json:"duration"`
	Timebase  model.Timebase `json:"timebase"`
	Payload   []byte         `json:"-"`
	// SHA256 is the digest of Payload. It is what verification compares
	// before and after remux, so the copy must be byte exact.
	SHA256 string `json:"sha256"`
	Sync   bool   `json:"sync"`
	// SourceOffset is the wrapper offset in the input file.
	SourceOffset uint64 `json:"source_offset"`
	Valid        bool   `json:"valid"`
}

// TrackData is the demuxed payload stream of one track.
type TrackData struct {
	Key      string          `json:"key"`
	Logical  string          `json:"logical"`
	Kind     model.TrackKind `json:"kind"`
	Timebase model.Timebase  `json:"timebase"`
	Packets  []*Packet       `json:"packets"`
	// Skipped counts fragments the probe listed whose bytes could not be
	// used (truncated or corrupt); each has a fault.
	Skipped int `json:"skipped"`
}

// Range is the packet range in track ticks.
func (t *TrackData) Range() model.Range {
	if len(t.Packets) == 0 {
		return model.Range{}
	}
	first := t.Packets[0]
	end := uint64(0)
	for _, p := range t.Packets {
		if p.StartTick+p.Duration > end {
			end = p.StartTick + p.Duration
		}
	}
	return model.Range{StartTick: first.StartTick, Duration: end - first.StartTick}
}

// Result is the demux output for one container.
type Result struct {
	Container model.ContainerKind `json:"container"`
	SHA256    string              `json:"sha256"`
	Tracks    []*TrackData        `json:"tracks"`
	Faults    fault.List          `json:"-"`
}

// ByKey returns a track's data.
func (r *Result) ByKey(key string) *TrackData {
	for _, t := range r.Tracks {
		if t.Key == key {
			return t
		}
	}
	return nil
}

// Demux reads every indexed fragment from the input and validates the payload
// against its essence header. A fragment whose bytes are not fully present or
// whose payload checksum fails is reported and skipped, never truncated to
// fit: silent truncation would make the output look complete while losing
// media.
func Demux(src probe.Source, f *probe.File) (*Result, error) {
	res := &Result{Container: f.Container, SHA256: src.Digest()}
	tracks := append([]*model.Track(nil), f.Tracks...)
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Key < tracks[j].Key })
	for _, t := range tracks {
		td, faults := demuxTrack(src, t)
		res.Tracks = append(res.Tracks, td)
		for _, e := range faults {
			res.Faults.Add(e)
		}
	}
	res.Faults = res.Faults.Dedup()
	return res, nil
}

func demuxTrack(src probe.Source, t *model.Track) (*TrackData, fault.List) {
	td := &TrackData{Key: t.Key, Logical: t.Logical, Kind: t.Kind, Timebase: t.Timebase}
	var faults fault.List
	data := src.Data
	for i := range t.Fragments {
		fr := t.Fragments[i]
		// The fragment must lie wholly inside the file. A short tail is a
		// truncation, not a smaller packet.
		if fr.PayloadOffset+fr.Size > uint64(len(data)) {
			faults.Add(fault.New(fault.TruncatedBox, fr.PayloadOffset,
				"fragment %d of %s needs %d bytes past EOF", fr.Seq, t.Key, fr.PayloadOffset+fr.Size-uint64(len(data))).
				WithTrack(t.Key, t.TrackID).WithRange(frRangeNS(td, fr)).WithRecoverable(false))
			td.Skipped++
			continue
		}
		payload := data[fr.PayloadOffset : fr.PayloadOffset+fr.Size]
		h, herr := essence.ParseHeader(payload)
		if herr != nil {
			faults.Add(fault.New(fault.CorruptSegment, fr.PayloadOffset,
				"fragment %d of %s has an unreadable essence header: %v", fr.Seq, t.Key, herr).
				WithTrack(t.Key, t.TrackID).WithRange(frRangeNS(td, fr)).WithRecoverable(false))
			td.Skipped++
			continue
		}
		if uint64(len(payload)) < h.TotalSize() {
			// The sample table promised more bytes than the frame declares:
			// the container and the essence disagree, so neither is trusted.
			faults.Add(fault.New(fault.TruncatedBox, fr.PayloadOffset,
				"fragment %d of %s declares %d bytes but the frame needs %d", fr.Seq, t.Key, len(payload), h.TotalSize()).
				WithTrack(t.Key, t.TrackID).WithRange(frRangeNS(td, fr)).WithRecoverable(false))
			td.Skipped++
			continue
		}
		// When the essence retains its RTP header, the RTP timestamp is an
		// independent statement of the frame's media time. A disagreement
		// with the container timeline means one of the two is wrong, so the
		// fragment is reported instead of copied into a confident-looking
		// output.
		if h.HasRTP() {
			rt, err := essence.ParseRTP(h, payload)
			if err != nil {
				faults.Add(fault.New(fault.CorruptSegment, fr.PayloadOffset,
					"fragment %d of %s has an unreadable RTP header: %v", fr.Seq, t.Key, err).
					WithTrack(t.Key, t.TrackID).WithRange(frRangeNS(td, fr)).WithRecoverable(false))
				td.Skipped++
				continue
			}
			if got := essence.RTPTimestampTicks(h, rt); got != fr.StartTick {
				faults.Add(fault.New(fault.CorruptSegment, fr.PayloadOffset,
					"fragment %d of %s container start %d != RTP timestamp %d", fr.Seq, t.Key, fr.StartTick, got).
					WithTrack(t.Key, t.TrackID).WithRange(frRangeNS(td, fr)).WithRecoverable(false))
				td.Skipped++
				continue
			}
		}
		ok, complete := essence.VerifyPayload(payload, h)
		if !complete || !ok {
			code := fault.CorruptSegment
			if !complete {
				code = fault.TruncatedBox
			}
			faults.Add(fault.New(code, fr.PayloadOffset,
				"fragment %d of %s fails essence validation", fr.Seq, t.Key).
				WithTrack(t.Key, t.TrackID).WithRange(frRangeNS(td, fr)).WithRecoverable(false))
			td.Skipped++
			continue
		}
		sum := sha256.Sum256(payload)
		td.Packets = append(td.Packets, &Packet{
			TrackKey: t.Key, TrackID: t.TrackID, Seq: fr.Seq,
			StartTick: fr.StartTick, Duration: fr.Duration, Timebase: t.Timebase,
			Payload: payload, SHA256: fmt.Sprintf("%x", sum[:]), Sync: fr.Sync,
			SourceOffset: fr.WrapperOffset, Valid: true,
		})
	}
	return td, faults
}

func frRangeNS(td *TrackData, fr model.Fragment) (int64, int64) {
	return td.Timebase.TickToNanos(fr.StartTick), td.Timebase.TickToNanos(fr.EndTick())
}

// Scan is the recovery fallback: when an index is missing or incomplete the
// recover package scans for frame boundaries. This helper exposes the same
// validation to the demux tests so both paths agree on what "complete" means.
func FrameComplete(payload []byte) bool {
	h, err := essence.ParseHeader(payload)
	if err != nil {
		return false
	}
	if uint64(len(payload)) < h.TotalSize() {
		return false
	}
	ok, complete := essence.VerifyPayload(payload, h)
	return complete && ok
}

// Uint32 reads a big-endian uint32, exported for boundary tests that inspect
// container wrappers directly.
func Uint32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
