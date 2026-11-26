package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"medtrace/internal/core"
)

// Scenario is a generated media work tree plus the expectations the verifier
// asserts against it. It is built entirely from the seed, so a repeated run
// with the same seed produces identical bytes and identical expectations.
type Scenario struct {
	Root string
	// DashMPD and HLSPlaylist are root-relative manifest paths.
	DashMPD     string
	HLSPlaylist string
	EventLog    string

	// Dash carries the six representation-split fragments in manifest order;
	// HLS carries the four playlist fragments.
	Dash []SegmentLayout
	HLS  []SegmentLayout

	// SamplesPerSeg and GOP describe the generated sample pattern.
	SamplesPerSeg int
	GOP           int

	// DashDuration is the total DASH presentation length.
	DashDuration core.Nanos
	// CorruptIndex is the DASH segment index the recovery stage damages.
	CorruptIndex int
	// CorruptSegment and CorruptOffset identify the damaged fragment and the
	// original offset at which its last sample begins.
	CorruptSegment string
	CorruptOffset  int64
	// ResumeSegment and ResumeOffset are the next segment's keyframe and the
	// original offset the index recorded for it.
	ResumeSegment string
	ResumeOffset  int64

	// EventMessages are the JSONL lines written to EventLog.
	EventMessages []string
}

// Segment sample geometry. Every segment starts with a sync sample so a
// recovery can resume from any segment boundary; the GOP covers a whole
// segment.
const (
	samplesPerSeg = 3
	segTicks      = 3600 // per-sample decode duration at 90 kHz (40 ms)
	targetSeg     = samplesPerSeg * segTicks
)

// Build generates a scenario under root:
//
//   - DASH: two video representations (v-low, v-high) of three 0.12 s
//     fragments each, numbered so a player switches representation after
//     three segments. The decode timeline is continuous across the switch.
//   - HLS: four fMP4 fragments sharing one init. A discontinuity precedes
//     segment 3, and segment 4 carries a 32-bit timestamp wrap.
//   - A client event log with an RTP timestamp that wraps.
//
// The DASH fragments start healthy; the verifier damages one after indexing so
// the indexed source positions stay authoritative.
func Build(root string, seed int64) (*Scenario, error) {
	low := TrackSpec{ID: 1, Timescale: 90000, Video: true, Width: 640, Height: 360}
	high := TrackSpec{ID: 2, Timescale: 90000, Video: true, Width: 1280, Height: 720}
	hlsTrack := TrackSpec{ID: 3, Timescale: 90000, Video: true, Width: 640, Height: 360}

	sc := &Scenario{
		Root: root, DashMPD: "dash/stream.mpd", HLSPlaylist: "hls/media.m3u8", EventLog: "events/client.jsonl",
		SamplesPerSeg: samplesPerSeg, GOP: 1,
		CorruptIndex: 1,
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}

	// DASH init segments.
	for _, spec := range []struct {
		path  string
		track TrackSpec
	}{
		{"dash/video/v-low/init.mp4", low},
		{"dash/video/v-high/init.mp4", high},
	} {
		if _, err := writeFile(root, spec.path, buildInit(spec.track)); err != nil {
			return nil, err
		}
	}
	// Seven fragments: indices 0..3 are v-low, 4..6 are v-high. The high
	// representation continues the low one's decode timeline so only the
	// representation changes at the midpoint (the representation-switch
	// anomaly). Seven segments with one damaged leaves six hash matches, which
	// is the segment_hashes=6/6 the verification contract asserts.
	const lowCount = 4
	for n := 0; n < 7; n++ {
		rep, spec, segNum := "v-low", low, n
		if n >= lowCount {
			rep, spec, segNum = "v-high", high, n-lowCount
		}
		samples := makeSamples(n)
		uri := fmt.Sprintf("dash/video/%s/seg-%03d.m4s", rep, segNum+1)
		data, lay := fragment(spec, uint32(n+1), int64(n*targetSeg), samples, seed,
			uri, "dash/video/"+rep+"/init.mp4", rep)
		if _, err := writeFile(root, uri, data); err != nil {
			return nil, err
		}
		sc.Dash = append(sc.Dash, lay)
	}
	sc.DashDuration = core.TicksToNanos(int64(7*targetSeg), 90000)

	// Record the damage and resume points from the intact layouts.
	bad := sc.Dash[sc.CorruptIndex]
	sc.CorruptSegment = bad.URI
	sc.CorruptOffset = bad.Samples[len(bad.Samples)-1].Offset
	resume := sc.Dash[sc.CorruptIndex+1]
	sc.ResumeSegment = resume.URI
	sc.ResumeOffset = resume.Samples[0].Offset

	if err := writeMPD(root); err != nil {
		return nil, err
	}

	// HLS init and fragments.
	if _, err := writeFile(root, "hls/init.mp4", buildInit(hlsTrack)); err != nil {
		return nil, err
	}
	// Segment 3 (index 2) starts a fresh timeline after the discontinuity;
	// segment 4 (index 3) wraps a 32-bit media-tick counter back near zero,
	// which the scanner classifies as a wrap rather than a backwards step.
	baseTimes := []int64{0, targetSeg, 4_294_000_000, 200}
	for i := 0; i < 4; i++ {
		samples := makeSamples(i)
		uri := fmt.Sprintf("hls/seg-%d.m4s", i)
		data, lay := fragment(hlsTrack, uint32(i+1), baseTimes[i], samples, seed+7, uri, "hls/init.mp4", "")
		if _, err := writeFile(root, uri, data); err != nil {
			return nil, err
		}
		sc.HLS = append(sc.HLS, lay)
	}
	if err := writeHLS(root); err != nil {
		return nil, err
	}
	if err := writeEvents(root, sc); err != nil {
		return nil, err
	}
	return sc, nil
}

// makeSamples builds one segment's samples. A sync sample leads every segment
// so a recovery can resume at any segment boundary.
func makeSamples(segIndex int) []SampleSpec {
	out := make([]SampleSpec, samplesPerSeg)
	for i := 0; i < samplesPerSeg; i++ {
		out[i] = SampleSpec{DurTicks: segTicks, Size: 220 + i*7 + segIndex*3, Key: i == 0}
	}
	return out
}

// Damage truncates the identified DASH fragment at its last sample, leaving
// the earlier samples and their offsets intact. It is called by the verifier
// after the index is built, so the indexed hashes and offsets no longer match
// the file on disk.
func (s *Scenario) Damage(root string) error {
	lay := s.Dash[s.CorruptIndex]
	abs, err := core.SafeJoin(root, lay.URI)
	if err != nil {
		return err
	}
	full, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	if int64(len(full)) <= lay.Samples[len(lay.Samples)-1].Offset {
		return fmt.Errorf("damage offset beyond file length")
	}
	return os.WriteFile(abs, full[:lay.Samples[len(lay.Samples)-1].Offset], 0o644)
}

// writeMPD writes a two-representation MPD whose template enumerates three
// fragments per representation.
func writeMPD(root string) error {
	// Each representation declares its own SegmentTimeline so the low
	// representation has four fragments and the high one three, which is what
	// leaves six healthy fragments after one is damaged.
	mpd := `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT0.84S">
  <Period id="p0">
    <AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1.42c01e">
      <Representation id="v-low" bandwidth="300000" width="640" height="360">
        <SegmentTemplate timescale="90000" startNumber="1"
          initialization="video/v-low/init.mp4" media="video/v-low/seg-$Number%03d$.m4s">
          <SegmentTimeline><S d="10800" r="3"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v-high" bandwidth="1200000" width="1280" height="720">
        <SegmentTemplate timescale="90000" startNumber="1"
          initialization="video/v-high/init.mp4" media="video/v-high/seg-$Number%03d$.m4s">
          <SegmentTimeline><S d="10800" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`
	return os.WriteFile(filepath.Join(root, "dash", "stream.mpd"), []byte(mpd), 0o644)
}

// writeHLS writes a media playlist with a discontinuity and program-date-time
// anchor before segment 3.
func writeHLS(root string) error {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i := 0; i < 4; i++ {
		if i == 2 {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
			b.WriteString("#EXT-X-PROGRAM-DATE-TIME:2024-01-01T00:00:09.000Z\n")
		}
		b.WriteString("#EXTINF:0.12,\n")
		fmt.Fprintf(&b, "seg-%d.m4s\n", i)
	}
	return os.WriteFile(filepath.Join(root, "hls", "media.m3u8"), []byte(b.String()), 0o644)
}

// writeEvents writes a client JSONL log whose final pair wraps a 90 kHz RTP
// timestamp across the 32-bit boundary.
func writeEvents(root string, sc *Scenario) error {
	lines := []string{
		`{"kind":"play","wall_unix_ms":1704067200000,"track":"video","representation":"v-low","position_nanos":0,"segment":"dash/video/v-low/seg-001.m4s","byte_offset":0}`,
		`{"kind":"request","wall_unix_ms":1704067200120,"track":"video","representation":"v-high","position_nanos":360000000,"segment":"dash/video/v-high/seg-001.m4s","byte_offset":0}`,
		`{"kind":"stall","wall_unix_ms":1704067200240,"track":"video","rtp_timestamp":4294967000,"rtp_clock_rate":90000,"segment":"hls/seg-2.m4s"}`,
		`{"kind":"resume","wall_unix_ms":1704067200360,"track":"video","rtp_timestamp":200,"rtp_clock_rate":90000,"segment":"hls/seg-3.m4s"}`,
	}
	sc.EventMessages = lines
	abs := filepath.Join(root, "events", "client.jsonl")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
