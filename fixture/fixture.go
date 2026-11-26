package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	rtpingest "archivecheck/rtp"
)

const (
	CaseNormal   = "normal"
	CaseCorrupt  = "corrupt"
	CaseRecovery = "recovery"
	CaseAll      = "all"
)

// Fixture is a fully self-generated local media/ingest sample.
type Fixture struct {
	Name          string
	Seed          int64
	DASHName      string
	DASH          string
	HLSName       string
	HLS           string
	Files         map[string][]byte
	Events        []rtpingest.Event
	EventPayloads map[string][]byte
}

// Build creates the requested deterministic sample.
func Build(name string, seed int64) *Fixture {
	f := &Fixture{
		Name:          name,
		Seed:          seed,
		DASHName:      "manifest.mpd",
		HLSName:       "index.m3u8",
		Files:         make(map[string][]byte),
		EventPayloads: make(map[string][]byte),
	}
	f.buildMedia()
	f.buildEvents()
	if name == CaseCorrupt {
		f.applyCorruption()
	}
	return f
}

func (f *Fixture) buildMedia() {
	for i := 0; i < 3; i++ {
		lowPath := "dash/low/seg-" + itoa(i+1) + ".m4s"
		highPath := "dash/high/seg-" + itoa(i+1) + ".m4s"
		hlsPath := "hls/seg-" + itoa(i) + ".m4s"
		f.Files[lowPath] = Fragment(f.Seed, i, 160+i*16)
		f.Files[highPath] = Fragment(f.Seed+100, i, 192+i*16)
		f.Files[hlsPath] = Fragment(f.Seed+200, i, 144+i*16)
	}

	f.DASH = `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" mediaPresentationDuration="PT6S" type="static">
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="video-low" bandwidth="800000">
        <SegmentTemplate timescale="1000" media="dash/low/seg-$Number$.m4s" startNumber="1">
          <SegmentTimeline><S t="0" d="2000" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="video-high" bandwidth="2500000">
        <SegmentTemplate timescale="1000" media="dash/high/seg-$Number$.m4s" startNumber="1">
          <SegmentTimeline><S t="0" d="2000" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`

	f.HLS = "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n" +
		"#EXTINF:2.000,\nhls/seg-0.m4s\n" +
		"#EXTINF:2.000,\nhls/seg-1.m4s\n" +
		"#EXT-X-DISCONTINUITY\n" +
		"#EXTINF:2.000,\nhls/seg-2.m4s\n"
}

func (f *Fixture) applyCorruption() {
	// One high-bitrate DASH fragment is damaged; the low representation at
	// the same timeline position remains available as a fallback.
	p := "dash/high/seg-2.m4s"
	f.Files[p] = Corrupt(f.Files[p], f.Seed)

	// One HLS fragment is truncated and has no alternate representation.
	h := "hls/seg-1.m4s"
	f.Files[h] = Truncate(f.Files[h])
}

func (f *Fixture) buildEvents() {
	baseArrival := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC).UnixNano()
	videoOrder := []uint16{100, 103, 101, 102, 104, 107, 105, 106, 107, 100}
	for i, seq := range videoOrder {
		payload := []byte("video-payload-" + itoa(int(seq)))
		digest := MustDigest(payload)
		f.EventPayloads[digest] = payload
		f.Events = append(f.Events, rtpingest.Event{
			Source:    "encoder-a",
			Kind:      "video",
			Track:     "video-main",
			Seq:       seq,
			PTSNS:     int64(seq-100) * 40_000_000,
			ArrivalNS: baseArrival + int64(i)*2_000_000,
			Digest:    digest,
		})
	}
	for i := 0; i < 8; i++ {
		payload := []byte("audio-payload-" + itoa(i))
		digest := MustDigest(payload)
		f.EventPayloads[digest] = payload
		f.Events = append(f.Events, rtpingest.Event{
			Source:    "encoder-a",
			Kind:      "audio",
			Track:     "audio-main",
			Seq:       uint16(200 + i),
			PTSNS:     int64(i)*40_000_000 + 5_000_000,
			ArrivalNS: baseArrival + int64(10+i)*2_000_000,
			Digest:    digest,
		})
	}
}

// Write materializes the generated sample under root.
func (f *Fixture) Write(root string) (dashPath, hlsPath, eventsPath string, err error) {
	for rel, data := range f.Files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", "", "", err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return "", "", "", err
		}
	}
	dashPath = filepath.Join(root, f.DASHName)
	hlsPath = filepath.Join(root, f.HLSName)
	eventsPath = filepath.Join(root, "events.jsonl")
	if err := os.WriteFile(dashPath, []byte(f.DASH), 0o644); err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(hlsPath, []byte(f.HLS), 0o644); err != nil {
		return "", "", "", err
	}
	var buf []byte
	for _, e := range f.Events {
		line, err := json.Marshal(e)
		if err != nil {
			return "", "", "", err
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(eventsPath, buf, 0o644); err != nil {
		return "", "", "", err
	}
	return dashPath, hlsPath, eventsPath, nil
}

func itoa(i int) string {
	// Small local helper avoids strconv in fixture call sites.
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
