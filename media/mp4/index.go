package mp4

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	gomp4 "github.com/abema/go-mp4"

	"replaykit/media/fault"
)

// Sample is one decodable video sample located in the input.
type Sample struct {
	Number     int64 `json:"number"`
	Offset     int64 `json:"offset"`
	Size       int64 `json:"size"`
	DecodeTime int64 `json:"decode_time"`
	Duration   int64 `json:"duration"`
	Key        bool  `json:"key"`
	Fragment   int   `json:"fragment"`
}

// End is the decode end in track ticks.
func (s Sample) End() int64 { return s.DecodeTime + s.Duration }

// Track is the video track index built from moov plus every moof.
type Track struct {
	ID        uint32   `json:"id"`
	Timescale uint32   `json:"timescale"`
	Samples   []Sample `json:"samples"`
}

// Index is a sample-accurate view of one fragmented MP4 rendition.
type Index struct {
	FileSize   int64           `json:"file_size"`
	Track      Track           `json:"track"`
	Keyframes  []int64         `json:"keyframes"`
	Complete   bool            `json:"complete"`
	Rebuilt    bool            `json:"rebuilt"`
	Digest     string          `json:"digest"`
	Anomalies  []fault.Anomaly `json:"anomalies"`
	CoveredEnd int64           `json:"covered_end"`
}

// BuildIndex parses moov and every moof/trun into an ordered sample index.
// A sidecar index is accepted only when its digest verifies; otherwise it is
// discarded and the index is rebuilt from fragments that are fully readable.
func BuildIndex(data []byte, scan *Scan, limits Limits, sidecar *Sidecar) (*Index, error) {
	if scan == nil {
		return nil, fault.Fatal("index_mp4", "", 0, fault.Range{}, fmt.Errorf("nil scan"))
	}
	track, err := parseTrack(data, scan, limits)
	if err != nil {
		return nil, err
	}
	idx := &Index{FileSize: int64(len(data)), Track: track}

	// The sidecar may only shortcut when it verifies. Any failure is
	// recorded and the fragment walk below rebuilds the authoritative list.
	if sidecar != nil {
		if err := sidecar.Verify(track); err != nil {
			idx.Rebuilt = true
			idx.Anomalies = append(idx.Anomalies, fault.New(trackName(track.ID), 0,
				fault.CategoryIndexChecksum, fault.SeverityWarning, fault.ActionRebuiltIndex,
				track.Timescale, fault.Range{}, err.Error()))
		}
	}

	idx.Anomalies = append(idx.Anomalies, scan.Anomalies...)
	samples, err := parseFragments(data, scan, limits, &idx.Anomalies)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, fault.Fatal("index_mp4", trackName(track.ID), 0, fault.Range{},
			fmt.Errorf("no readable samples in any moof"))
	}
	idx.Track.Samples = samples
	idx.Complete = len(idx.Anomalies) == 0
	idx.Rebuilt = idx.Rebuilt || !idx.Complete
	for _, s := range samples {
		if s.Key {
			idx.Keyframes = append(idx.Keyframes, s.DecodeTime)
		}
		if s.End() > idx.CoveredEnd {
			idx.CoveredEnd = s.End()
		}
	}
	idx.Digest = idx.computeDigest()
	return idx, nil
}

func trackName(id uint32) string { return fmt.Sprintf("video-track-%d", id) }

// parseTrack reads mdhd timescale and tkhd track id from moov. Without a
// usable timescale no time mapping exists, which is fatal for the task.
func parseTrack(data []byte, scan *Scan, limits Limits) (Track, error) {
	var tr Track
	mdhdRef, ok := scan.First("mdhd")
	if !ok {
		return tr, fault.Fatal("index_mp4", "", 0, fault.Range{}, fmt.Errorf("moov/mdhd missing: no time mapping"))
	}
	payload, err := scan.Payload(data, mdhdRef)
	if err != nil {
		return tr, fault.Fatal("index_mp4", "", mdhdRef.Offset, fault.Range{}, err)
	}
	mdhd := &gomp4.Mdhd{}
	if _, err := Unmarshal(payload, mdhd); err != nil {
		return tr, fault.Fatal("index_mp4", "", mdhdRef.Offset, fault.Range{}, fmt.Errorf("mdhd: %w", err))
	}
	if mdhd.Timescale == 0 {
		return tr, fault.Fatal("index_mp4", "", mdhdRef.Offset, fault.Range{},
			fmt.Errorf("mdhd timescale is zero: no time mapping"))
	}
	tr.Timescale = mdhd.Timescale
	if tkhdRef, ok := scan.First("tkhd"); ok {
		if payload, err := scan.Payload(data, tkhdRef); err == nil {
			tkhd := &gomp4.Tkhd{}
			if _, err := Unmarshal(payload, tkhd); err == nil {
				tr.ID = tkhd.TrackID
			}
		}
	}
	return tr, nil
}

// defaults carries the trex and tfhd sample defaults for one fragment.
type defaults struct {
	duration uint32
	size     uint32
	flags    uint32
}

func parseFragments(data []byte, scan *Scan, limits Limits, anomalies *[]fault.Anomaly) ([]Sample, error) {
	trex := trexDefaults(data, scan)
	var samples []Sample
	fragNo := 0
	for _, moof := range scan.TopLevelOf("moof") {
		fragNo++
		moofPayload, err := scan.Payload(data, moof)
		if err != nil {
			*anomalies = append(*anomalies, fault.New("", moof.Offset, fault.CategorySampleOverflow,
				fault.SeverityError, fault.ActionSkipped, 0, fault.Range{}, err.Error()))
			continue
		}
		mf := &gomp4.Moof{}
		if _, err := Unmarshal(moofPayload, mf); err != nil {
			*anomalies = append(*anomalies, fault.New("", moof.Offset, fault.CategoryMalformedBox,
				fault.SeverityError, fault.ActionSkipped, 0, fault.Range{}, "moof: "+err.Error()))
			continue
		}
		trafs := childBoxes(scan, moof, "traf")
		for _, traf := range trafs {
			frag, ok := parseTraf(data, scan, traf, moof.Offset, trex, limits, anomalies, fragNo)
			if !ok {
				continue
			}
			if len(frag) > 0 {
				samples = append(samples, frag...)
			}
		}
	}
	for i := range samples {
		samples[i].Number = int64(i + 1)
	}
	return samples, nil
}

// childBoxes returns the direct children of parent with the given type,
// decided by byte containment rather than by repeated path strings.
func childBoxes(scan *Scan, parent BoxRef, typ string) []BoxRef {
	var out []BoxRef
	for _, b := range scan.DirectChildren(parent) {
		if b.Type == typ {
			out = append(out, b)
		}
	}
	return out
}

func trexDefaults(data []byte, scan *Scan) defaults {
	ref, ok := scan.First("trex")
	if !ok {
		return defaults{}
	}
	payload, err := scan.Payload(data, ref)
	if err != nil {
		return defaults{}
	}
	trex := &gomp4.Trex{}
	if _, err := Unmarshal(payload, trex); err != nil {
		return defaults{}
	}
	return defaults{duration: trex.DefaultSampleDuration, size: trex.DefaultSampleSize, flags: trex.DefaultSampleFlags}
}

// parseTraf expands one traf into samples, validating every byte range
// against the input. Out-of-range samples invalidate the fragment, which is
// then skipped rather than partially trusted.
func parseTraf(data []byte, scan *Scan, traf BoxRef, moofOffset int64, trex defaults, limits Limits,
	anomalies *[]fault.Anomaly, fragNo int) ([]Sample, bool) {
	var tfhd gomp4.Tfhd
	var base uint64 = uint64(moofOffset)
	def := trex
	tfhdRef, ok := firstChild(scan, traf, "tfhd")
	if !ok {
		return nil, false
	}
	if p, err := scan.Payload(data, tfhdRef); err == nil {
		if _, err := Unmarshal(p, &tfhd); err != nil {
			*anomalies = append(*anomalies, fault.New("", tfhdRef.Offset, fault.CategoryMalformedBox,
				fault.SeverityError, fault.ActionSkipped, 0, fault.Range{}, "tfhd: "+err.Error()))
			return nil, false
		}
	} else {
		return nil, false
	}
	flags := tfhd.GetFlags()
	if flags&gomp4.TfhdBaseDataOffsetPresent != 0 {
		base = tfhd.BaseDataOffset
	}
	if flags&gomp4.TfhdDefaultSampleDurationPresent != 0 {
		def.duration = tfhd.DefaultSampleDuration
	}
	if flags&gomp4.TfhdDefaultSampleSizePresent != 0 {
		def.size = tfhd.DefaultSampleSize
	}
	if flags&gomp4.TfhdDefaultSampleFlagsPresent != 0 {
		def.flags = tfhd.DefaultSampleFlags
	}

	decode := uint64(0)
	if dtRef, ok := firstChild(scan, traf, "tfdt"); ok {
		if p, err := scan.Payload(data, dtRef); err == nil {
			dt := &gomp4.Tfdt{}
			if _, err := Unmarshal(p, dt); err == nil {
				decode = dt.GetBaseMediaDecodeTime()
			}
		}
	}

	trunRefs := children(scan, traf, "trun")
	if len(trunRefs) == 0 {
		return nil, false
	}
	var out []Sample
	number := int64(0)
	for _, trRef := range trunRefs {
		p, err := scan.Payload(data, trRef)
		if err != nil {
			*anomalies = append(*anomalies, fault.New("", trRef.Offset, fault.CategorySampleOverflow,
				fault.SeverityError, fault.ActionSkipped, 0, fault.Range{}, err.Error()))
			return nil, false
		}
		tr := &gomp4.Trun{}
		if _, err := Unmarshal(p, tr); err != nil {
			*anomalies = append(*anomalies, fault.New("", trRef.Offset, fault.CategoryMalformedBox,
				fault.SeverityError, fault.ActionSkipped, 0, fault.Range{}, "trun: "+err.Error()))
			return nil, false
		}
		tflags := tr.GetFlags()
		cursor := int64(base)
		if tflags&0x000001 != 0 {
			cursor = int64(base) + int64(tr.DataOffset)
		}
		for i := 0; i < int(tr.SampleCount); i++ {
			var entry gomp4.TrunEntry
			if i < len(tr.Entries) {
				entry = tr.Entries[i]
			}
			dur := def.duration
			if tflags&0x000100 != 0 {
				dur = entry.SampleDuration
			}
			sz := def.size
			if tflags&0x000200 != 0 {
				sz = entry.SampleSize
			}
			sflags := def.flags
			if tflags&0x000400 != 0 {
				sflags = entry.SampleFlags
			}
			if i == 0 && tflags&0x000004 != 0 {
				sflags = tr.FirstSampleFlags
			}
			number++
			end := cursor + int64(sz)
			if sz == 0 || cursor < 0 || end > int64(len(data)) {
				*anomalies = append(*anomalies, fault.New("", trRef.Offset, fault.CategorySampleOverflow,
					fault.SeverityError, fault.ActionSkipped, 0, fault.Range{},
					fmt.Sprintf("sample %d range [%d,%d) outside input of %d bytes", number, cursor, end, len(data))))
				return nil, false
			}
			out = append(out, Sample{
				Number:     number,
				Offset:     cursor,
				Size:       int64(sz),
				DecodeTime: int64(decode),
				Duration:   int64(dur),
				Key:        sflags&0x00010000 == 0,
				Fragment:   fragNo,
			})
			decode += uint64(dur)
			cursor = end
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DecodeTime != out[j].DecodeTime {
			return out[i].DecodeTime < out[j].DecodeTime
		}
		return out[i].Offset < out[j].Offset
	})
	return out, len(out) > 0
}

func firstChild(scan *Scan, parent BoxRef, typ string) (BoxRef, bool) {
	return scan.DirectChild(parent, typ)
}

func children(scan *Scan, parent BoxRef, typ string) []BoxRef { return childBoxes(scan, parent, typ) }

// computeDigest is the deterministic SHA-256 over the canonical sample list.
func (idx *Index) computeDigest() string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|track=%d|ts=%d|n=%d\n", idx.Track.ID, idx.Track.Timescale, len(idx.Track.Samples))
	for _, s := range idx.Track.Samples {
		fmt.Fprintf(h, "%d,%d,%d,%d,%d,%t,%d\n", s.Number, s.Offset, s.Size, s.DecodeTime, s.Duration, s.Key, s.Fragment)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// KeyframeAtOrBefore returns the last keyframe at or before tick.
func (idx *Index) KeyframeAtOrBefore(tick int64) (Sample, bool) {
	var best Sample
	found := false
	for _, s := range idx.Track.Samples {
		if !s.Key || s.DecodeTime > tick {
			continue
		}
		if !found || s.DecodeTime > best.DecodeTime {
			best, found = s, true
		}
	}
	return best, found
}

// KeyframeAtOrAfter returns the first keyframe at or after tick.
func (idx *Index) KeyframeAtOrAfter(tick int64) (Sample, bool) {
	for _, s := range idx.Track.Samples {
		if s.Key && s.DecodeTime >= tick {
			return s, true
		}
	}
	return Sample{}, false
}

// SampleContaining returns the sample whose decode interval holds tick.
func (idx *Index) SampleContaining(tick int64) (Sample, bool) {
	for _, s := range idx.Track.Samples {
		if tick >= s.DecodeTime && tick < s.End() {
			return s, true
		}
	}
	return Sample{}, false
}

// SamplesInRange returns samples overlapping [start,end) in decode order.
func (idx *Index) SamplesInRange(start, end int64) []Sample {
	var out []Sample
	for _, s := range idx.Track.Samples {
		if s.End() > start && s.DecodeTime < end {
			out = append(out, s)
		}
	}
	return out
}

// MarshalCanonical renders the index deterministically for checkpointing.
func (idx *Index) MarshalCanonical() ([]byte, error) {
	type canonical struct {
		Digest    string   `json:"digest"`
		Timescale uint32   `json:"timescale"`
		Complete  bool     `json:"complete"`
		Rebuilt   bool     `json:"rebuilt"`
		Samples   []Sample `json:"samples"`
	}
	return json.Marshal(canonical{
		Digest: idx.Digest, Timescale: idx.Track.Timescale,
		Complete: idx.Complete, Rebuilt: idx.Rebuilt, Samples: idx.Track.Samples,
	})
}
