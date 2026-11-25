package continuity

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/abema/go-mp4"
)

// The mux re-emits the recomputed program as fresh CMAF init segments and
// media fragments. Sample payloads, codec configuration and integer timing are
// copied unchanged: no transcode and no re-quantisation. Timing that cannot be
// expressed exactly in the target track timescale is rejected rather than
// rounded.

// OutputSegmentTicks is the nominal output fragment duration (2 s).
const OutputSegmentTicks Ticks = 2 * Ticks(BaseTimescale)

// MuxOutput is the in-memory remuxed program plus its derived manifests.
type MuxOutput struct {
	// Init holds one init segment per target track key.
	Init map[string][]byte
	// Segments holds fragments per target track key, in presentation order.
	Segments map[string][][]byte
	// HLS holds one media playlist per target track key.
	HLS map[string][]byte
	// DASH is the output MPD covering every target track.
	DASH []byte
	// Starts and Durations hold per-track fragment timing.
	Starts    map[string][]Ticks
	Durations map[string][]Ticks
	// OutBytes maps every output sample to its byte range inside the output
	// fragment. Keys are "target|dts|pts", which is unique per program.
	OutBytes map[string]ByteRange
	// TrackIDs maps a target key to its output track_ID.
	TrackIDs map[string]uint32
}

// MuxProgram remuxes the program in memory, cutting one fragment per track each
// segTicks of presentation time.
func MuxProgram(prog *Program, segTicks Ticks) (*MuxOutput, error) {
	if prog == nil {
		return nil, Usagef("mux requires a program")
	}
	if segTicks <= 0 {
		segTicks = OutputSegmentTicks
	}
	tracks := append([]TargetTrack(nil), prog.Tracks...)
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Key < tracks[j].Key })
	ids := make(map[string]uint32, len(tracks))
	for _, t := range tracks {
		ids[t.Key] = trackIDFor(t.Key)
	}
	out := &MuxOutput{
		Init:      map[string][]byte{},
		Segments:  map[string][][]byte{},
		HLS:       map[string][]byte{},
		Starts:    map[string][]Ticks{},
		Durations: map[string][]Ticks{},
		OutBytes:  map[string]ByteRange{},
		TrackIDs:  ids,
	}
	for _, track := range tracks {
		init := writeInitBytes([]TargetTrack{track})
		if len(init) == 0 {
			return nil, NewError(KindMux, Location{Offset: -1, Sample: -1}, "cannot build init segment for %s", track.Key)
		}
		out.Init[track.Key] = init
		var samples []PlacedSample
		for _, s := range prog.Samples {
			if s.Target == track.Key {
				samples = append(samples, s)
			}
		}
		if len(samples) == 0 {
			return nil, NewError(KindMux, Location{Offset: -1, Sample: -1}, "target track %s has no samples", track.Key)
		}
		sort.SliceStable(samples, func(i, j int) bool {
			if samples[i].DTS != samples[j].DTS {
				return samples[i].DTS < samples[j].DTS
			}
			return samples[i].PTS < samples[j].PTS
		})
		groups := splitFragments(samples, segTicks)
		for idx, group := range groups {
			spec, baseDTS, startTicks, durTicks, err := fragmentFor(track, group)
			if err != nil {
				return nil, err
			}
			spec.SequenceNumber = uint32(idx + 1)
			data, err := encodeFragment(spec)
			if err != nil {
				return nil, NewError(KindMux, Location{File: samples[0].Segment, Segment: samples[0].Segment, Offset: samples[0].ByteOffset, Sample: group[0].Index},
					"encode fragment for %s: %v", track.Key, err)
			}
			if _, err := Boxes(data); err != nil {
				return nil, NewError(KindMux, Location{Offset: -1, Sample: -1}, "encoded fragment for %s is malformed: %v", track.Key, err)
			}
			recordOutputRanges(out, track.Key, idx, data, group)
			out.Segments[track.Key] = append(out.Segments[track.Key], data)
			out.Starts[track.Key] = append(out.Starts[track.Key], startTicks)
			out.Durations[track.Key] = append(out.Durations[track.Key], durTicks)
			_ = baseDTS
		}
		out.HLS[track.Key] = HLSPlaylist(track.Key, out.Starts[track.Key], out.Durations[track.Key], segTicks)
	}
	out.DASH = DASHManifest(tracks, out.Starts, out.Durations)
	return out, nil
}

// recordOutputRanges re-demuxes one encoded fragment to map every source sample
// to its exact output byte range.
func recordOutputRanges(out *MuxOutput, key string, fragIndex int, data []byte, group []PlacedSample) {
	// The fragment was produced from exactly this group, so the parsed sample
	// order matches the group's order.
	init := &InitSegment{}
	_ = init
	boxes, err := Boxes(data)
	if err != nil {
		return
	}
	var mdatStart int64 = -1
	for _, b := range boxes {
		if b.Type == "mdat" {
			mdatStart = b.Offset + b.Header
			break
		}
	}
	if mdatStart < 0 {
		return
	}
	cursor := mdatStart
	for _, s := range group {
		out.OutBytes[OutputSampleKey(key, s)] = ByteRange{Start: cursor, End: cursor + int64(len(s.Data))}
		cursor += int64(len(s.Data))
	}
	_ = fragIndex
}

// OutputSampleKey identifies an output sample within MuxOutput.OutBytes.
func OutputSampleKey(trackKey string, s PlacedSample) string {
	return fmt.Sprintf("%s|%d|%d", trackKey, s.DTS, s.PTS)
}

// splitFragments groups decode-ordered samples into fragments of at most
// segTicks, cutting on sample boundaries so no sample is split.
func splitFragments(samples []PlacedSample, segTicks Ticks) [][]PlacedSample {
	var groups [][]PlacedSample
	segStart := samples[0].DTS
	cur := []PlacedSample{}
	for _, s := range samples {
		if len(cur) > 0 && s.DTS >= segStart+segTicks {
			groups = append(groups, cur)
			cur = nil
			segStart = s.DTS
		}
		cur = append(cur, s)
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// fragmentFor converts a group of placed samples into an exact fragment spec.
func fragmentFor(track TargetTrack, group []PlacedSample) (FragmentSpec, uint64, Ticks, Ticks, error) {
	loc := Location{File: group[0].Segment, Segment: group[0].Segment, Offset: group[0].ByteOffset, Sample: group[0].Index}
	baseDTS64, err := Rescale(int64(group[0].DTS), BaseTimescale, track.Timescale)
	if err != nil {
		return FragmentSpec{}, 0, 0, 0, NewError(KindMux, loc, "fragment base time does not fit timescale %d: %v", track.Timescale, err)
	}
	baseDTS := uint64(baseDTS64)
	back, err := Rescale(baseDTS64, track.Timescale, BaseTimescale)
	if err != nil || Ticks(back) != group[0].DTS {
		return FragmentSpec{}, 0, 0, 0, NewError(KindMux, loc,
			"fragment start %d ticks cannot be represented exactly in timescale %d", group[0].DTS, track.Timescale)
	}
	spec := FragmentSpec{TrackID: trackIDFor(track.Key), Timescale: track.Timescale, BaseDecodeTime: baseDTS}
	startTicks := group[0].PTS
	var end Ticks
	for i, s := range group {
		if i == 0 || s.PTS < startTicks {
			startTicks = s.PTS
		}
		if i == 0 || s.End() > end {
			end = s.End()
		}
		dur, err := Rescale(int64(s.Duration), BaseTimescale, track.Timescale)
		if err != nil || dur <= 0 || (s.Duration > 0 && Ticks(dur) == 0) {
			return FragmentSpec{}, 0, 0, 0, NewError(KindMux, loc, "sample duration %d does not fit timescale %d", s.Duration, track.Timescale)
		}
		backDur, _ := Rescale(dur, track.Timescale, BaseTimescale)
		if Ticks(backDur) != s.Duration {
			return FragmentSpec{}, 0, 0, 0, NewError(KindMux, loc,
				"sample duration %d ticks cannot be represented exactly in timescale %d", s.Duration, track.Timescale)
		}
		cto, err := Rescale(int64(s.PTS-s.DTS), BaseTimescale, track.Timescale)
		if err != nil {
			return FragmentSpec{}, 0, 0, 0, NewError(KindMux, loc, "composition offset does not fit timescale %d", track.Timescale)
		}
		backCto, _ := Rescale(cto, track.Timescale, BaseTimescale)
		if Ticks(backCto) != s.PTS-s.DTS {
			return FragmentSpec{}, 0, 0, 0, NewError(KindMux, loc,
				"composition offset %d ticks cannot be represented exactly in timescale %d", s.PTS-s.DTS, track.Timescale)
		}
		spec.Samples = append(spec.Samples, SampleSpec{
			Data:              s.Data,
			Duration:          uint32(dur),
			CompositionOffset: int32(cto),
			Sync:              s.Sync,
		})
	}
	return spec, baseDTS, startTicks, end - startTicks, nil
}

// writeSeeker adapts a byte slice to io.WriteSeeker for go-mp4's writer.
type writeSeeker struct {
	buf []byte
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(len(w.buf)) {
		grown := make([]byte, end)
		copy(grown, w.buf)
		w.buf = grown
	}
	copy(w.buf[w.pos:], p)
	w.pos = end
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = w.pos + offset
	case io.SeekEnd:
		abs = int64(len(w.buf)) + offset
	default:
		return 0, fmt.Errorf("invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("negative seek position %d", abs)
	}
	if abs > int64(len(w.buf)) {
		grown := make([]byte, abs)
		copy(grown, w.buf)
		w.buf = grown
	}
	w.pos = abs
	return abs, nil
}

func fullBox(version uint8, flags uint32) mp4.FullBox {
	var fb mp4.FullBox
	fb.Version = version
	fb.Flags = [3]byte{byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return fb
}

// encodeFragment renders moof+mdat with go-mp4's box writer, patching trun's
// data_offset to point at the first sample byte inside mdat.
func encodeFragment(spec FragmentSpec) ([]byte, error) {
	if spec.SequenceNumber == 0 {
		spec.SequenceNumber = 1
	}
	build := func(dataOffset int32) ([]byte, error) {
		ws := &writeSeeker{}
		w := mp4.NewWriter(ws)
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
			return nil, err
		}
		mfhd := &mp4.Mfhd{FullBox: fullBox(0, 0), SequenceNumber: spec.SequenceNumber}
		if err := writePayload(w, mfhd); err != nil {
			return nil, err
		}
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
			return nil, err
		}
		tfhd := &mp4.Tfhd{FullBox: fullBox(0, mp4.TfhdDefaultBaseIsMoof), TrackID: spec.TrackID}
		if err := writePayload(w, tfhd); err != nil {
			return nil, err
		}
		tfdt := &mp4.Tfdt{FullBox: fullBox(1, 0), BaseMediaDecodeTimeV1: spec.BaseDecodeTime}
		if err := writePayload(w, tfdt); err != nil {
			return nil, err
		}
		trun := &mp4.Trun{
			FullBox:     fullBox(1, 0x000001|0x000100|0x000200|0x000400|0x000800),
			SampleCount: uint32(len(spec.Samples)),
			DataOffset:  dataOffset,
		}
		for _, s := range spec.Samples {
			flags := uint32(flagSync)
			if !s.Sync {
				flags = flagNonSync
			}
			trun.Entries = append(trun.Entries, mp4.TrunEntry{
				SampleDuration:                s.Duration,
				SampleSize:                    uint32(len(s.Data)),
				SampleFlags:                   flags,
				SampleCompositionTimeOffsetV1: s.CompositionOffset,
			})
		}
		if err := writePayload(w, trun); err != nil {
			return nil, err
		}
		if _, err := w.EndBox(); err != nil { // traf
			return nil, err
		}
		if _, err := w.EndBox(); err != nil { // moof
			return nil, err
		}
		mdat := &mp4.Mdat{}
		for _, s := range spec.Samples {
			mdat.Data = append(mdat.Data, s.Data...)
		}
		if err := writePayload(w, mdat); err != nil {
			return nil, err
		}
		return ws.buf, nil
	}
	probe, err := build(0)
	if err != nil {
		return nil, err
	}
	boxes, err := Boxes(probe)
	if err != nil || len(boxes) == 0 || boxes[0].Type != "moof" {
		return nil, errors.New("encoded moof is malformed")
	}
	return build(int32(boxes[0].Size + 8))
}

func writePayload(w *mp4.Writer, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: box.GetType()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// writeInitBytes renders ftyp+moov for the targets with the deterministic
// builder, so init bytes are byte-identical for identical targets.
func writeInitBytes(tracks []TargetTrack) []byte {
	specs := make([]TrackSpec, 0, len(tracks))
	for _, t := range tracks {
		spec := TrackSpec{
			ID:           trackIDFor(t.Key),
			Kind:         t.Kind,
			Timescale:    t.Timescale,
			CodecConfig:  t.CodecConfig,
			Width:        t.Width,
			Height:       t.Height,
			ChannelCount: t.ChannelCount,
			SampleRate:   t.SampleRate,
		}
		switch t.Codec {
		case CodecAVC:
			spec.SampleEntry = "avc1"
		case CodecHEVC:
			spec.SampleEntry = "hvc1"
		case CodecAAC:
			spec.SampleEntry = "mp4a"
		}
		specs = append(specs, spec)
	}
	return BuildInit(InitSpec{Tracks: specs})
}

// trackIDFor assigns a deterministic, non-zero track_ID from the target key.
func trackIDFor(key string) uint32 {
	h := sha256.Sum256([]byte("trackid/" + key))
	id := uint32(h[0])<<8 | uint32(h[1])
	if id == 0 {
		id = 1
	}
	return id
}

// DigestInput serialises the output for stable hashing.
func (m *MuxOutput) DigestInput() []byte {
	var b bytes.Buffer
	b.WriteString("continuity/media/v1\n")
	keys := make([]string, 0, len(m.Init))
	for k := range m.Init {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "init|%s|%d\n", k, len(m.Init[k]))
		b.Write(m.Init[k])
		for i, seg := range m.Segments[k] {
			fmt.Fprintf(&b, "seg|%s|%d|%d\n", k, i, len(seg))
			b.Write(seg)
		}
		if hls, ok := m.HLS[k]; ok {
			fmt.Fprintf(&b, "hls|%s|%d\n", k, len(hls))
			b.Write(hls)
		}
	}
	fmt.Fprintf(&b, "dash|%d\n", len(m.DASH))
	b.Write(m.DASH)
	return b.Bytes()
}

// SegmentName returns the deterministic file name of an output fragment.
func SegmentName(trackKey string, index int) string {
	return fmt.Sprintf("%s/seg-%05d.m4s", trackKey, index)
}

// HLSPlaylist renders a deterministic HLS media playlist for one track. A
// segment whose start does not touch the previous segment's end is preceded by
// EXT-X-DISCONTINUITY, HLS's explicit timeline break, so the playlist never
// claims coverage that the media does not have.
func HLSPlaylist(trackKey string, starts, segmentTicks []Ticks, target Ticks) []byte {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	targetSecs := int((target + Ticks(BaseTimescale) - 1) / Ticks(BaseTimescale))
	if targetSecs < 1 {
		targetSecs = 1
	}
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetSecs)
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s/init.mp4\"\n", trackKey)
	for i, dur := range segmentTicks {
		if i > 0 && starts[i] != starts[i-1]+segmentTicks[i-1] {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", float64(dur)/float64(BaseTimescale), SegmentName(trackKey, i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return []byte(b.String())
}

// DASHManifest renders a deterministic MPD for the targets. Per-track fragment
// start/duration ticks are emitted verbatim so gaps stay explicit.
func DASHManifest(tracks []TargetTrack, starts map[string][]Ticks, durs map[string][]Ticks) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	total := Ticks(0)
	for _, t := range tracks {
		for i := range durs[t.Key] {
			if end := starts[t.Key][i] + durs[t.Key][i]; end > total {
				total = end
			}
		}
	}
	fmt.Fprintf(&b, `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT%.3fS">`+"\n",
		float64(total)/float64(BaseTimescale))
	b.WriteString("  <Period id=\"p0\" start=\"PT0S\">\n")
	for _, t := range tracks {
		content := "audio"
		mime := "audio/mp4"
		if t.Kind == MediaVideo {
			content = "video"
			mime = "video/mp4"
		}
		fmt.Fprintf(&b, "    <AdaptationSet contentType=%q mimeType=%q>\n", content, mime)
		fmt.Fprintf(&b, "      <Representation id=%q bandwidth=\"1\" codecs=%q>\n", t.Key, string(t.Codec))
		fmt.Fprintf(&b, "        <SegmentTemplate timescale=\"%d\" startNumber=\"0\" initialization=\"%s/init.mp4\" media=\"%s/seg-$Number%%05d$.m4s\">\n",
			BaseTimescale, t.Key, t.Key)
		b.WriteString("          <SegmentTimeline>\n")
		for i := range starts[t.Key] {
			fmt.Fprintf(&b, "            <S t=\"%d\" d=\"%d\"/>\n", starts[t.Key][i], durs[t.Key][i])
		}
		b.WriteString("          </SegmentTimeline>\n        </SegmentTemplate>\n      </Representation>\n    </AdaptationSet>\n")
	}
	b.WriteString("  </Period>\n</MPD>\n")
	return []byte(b.String())
}
