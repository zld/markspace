// Package fixtures generates the self-contained sample set used by the
// verify command: a healthy presentation, an envelope-only variant, a
// single-event variant, corrupted files and a legacy manifest.
package fixtures

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"replaypipe/internal/metadata"
	"replaypipe/internal/mp4box"
)

// Timeline constants shared by the fixture and its expected results.
const (
	Timescale = 90000
	FPS       = 30
	FrameDur  = Timescale / FPS // 3000 ticks
	GOP       = 40              // 8 independent keyframes over 300 frames
	Frames    = 300             // 10s presentation
	Width     = 1280
	Height    = 720
)

// EventSeconds lists the three review events written into the side-car
// subtitle file, chosen so each maps to a distinct keyframe.
var EventSeconds = []struct {
	Start float64
	End   float64
	Text  string
}{
	{2.0, 4.0, "kickoff"},
	{6.0, 8.0, "counter attack"},
	{8.5, 9.5, "final whistle"},
}

// FileNames are the generated fixture names.
const (
	NormalFile      = "normal.mp4"
	EnvelopeFile    = "envelope.mp4"
	EventFile       = "event.mp4"
	TruncatedFile   = "truncated.mp4"
	RegressionFile  = "tfdt-regression.mp4"
	OverflowFile    = "time-overflow.mp4"
	OutOfRangeFile  = "event-out-of-range.mp4"
	SRTFile         = "events.srt"
	VTTFile         = "events.vtt"
	ChapterFile     = "chapters.json"
	ID3File         = "events.id3"
	SEIFile         = "events.sei"
	RTPFile         = "events.rtp"
	LegacyManifest  = "legacy-manifest.json"
	EventVariantSRT = "events-variant.srt"
)

// Set is the generated sample directory layout.
type Set struct {
	Root     string
	Normal   string
	Envelope string
	Event    string
	SRT      string
	VTT      string
	Chapters string
	ID3      string
	SEI      string
	RTP      string
	Legacy   string
	Variant  string
	Corrupt  []Corrupt
}

// TruncatedPath returns the truncated fixture path from a generated set.
func TruncatedPath(set *Set) string {
	for _, c := range set.Corrupt {
		if c.Name == TruncatedFile {
			return c.Path
		}
	}
	return ""
}

// Corrupt describes one defect fixture and the code it must produce.
type Corrupt struct {
	Name string
	Path string
	Code string
}

// Generate writes all fixtures under root and returns their paths.
func Generate(root string) (*Set, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	set := &Set{
		Root:     root,
		Normal:   filepath.Join(root, NormalFile),
		Envelope: filepath.Join(root, EnvelopeFile),
		Event:    filepath.Join(root, EventFile),
		SRT:      filepath.Join(root, SRTFile),
		VTT:      filepath.Join(root, VTTFile),
		Chapters: filepath.Join(root, ChapterFile),
		ID3:      filepath.Join(root, ID3File),
		SEI:      filepath.Join(root, SEIFile),
		RTP:      filepath.Join(root, RTPFile),
		Legacy:   filepath.Join(root, LegacyManifest),
		Variant:  filepath.Join(root, EventVariantSRT),
	}
	tracks := BuildTracks()
	if err := os.WriteFile(set.Normal, mp4box.Build(tracks, mp4box.DefaultOpts()), 0o644); err != nil {
		return nil, err
	}
	envOpts := mp4box.DefaultOpts()
	envOpts.CompatibleBrands = []string{"isom", "iso6", "mp41", "cmfc", "cmf2"}
	envOpts.FreePadding = 64
	if err := os.WriteFile(set.Envelope, mp4box.Build(tracks, envOpts), 0o644); err != nil {
		return nil, err
	}
	// The event-only variant keeps identical media; only side-car cue text
	// differs, so exactly one slice must be recomputed.
	if err := os.WriteFile(set.Event, mp4box.Build(tracks, mp4box.DefaultOpts()), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(set.SRT, []byte(srtText(EventSeconds, false)), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(set.Variant, []byte(srtText(variantEvents(), true)), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(set.VTT, []byte(vttText()), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(set.Chapters, []byte(chapterText()), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(set.ID3, buildID3(), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(set.SEI, buildSEI(), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(set.RTP, buildRTP(), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(set.Legacy, []byte(legacyManifest()), 0o644); err != nil {
		return nil, err
	}
	corrupt, err := writeCorrupt(root, set.Normal)
	if err != nil {
		return nil, err
	}
	set.Corrupt = corrupt
	return set, nil
}

func variantEvents() []struct {
	Start float64
	End   float64
	Text  string
} {
	return []struct {
		Start float64
		End   float64
		Text  string
	}{
		{2.0, 4.0, "kickoff"},
		{6.0, 8.0, "counter attack"},
		{8.5, 9.5, "final whistle (revised)"},
	}
}

func srtText(events []struct {
	Start float64
	End   float64
	Text  string
}, variant bool) string {
	out := ""
	for i, e := range events {
		out += fmt.Sprintf("%d\r\n%s --> %s\r\n%s\r\n\r\n", i+1, srtClock(e.Start), srtClock(e.End), e.Text)
	}
	return out
}

func srtClock(sec float64) string {
	ms := int64(sec*1000 + 0.5)
	h := ms / 3_600_000
	ms -= h * 3_600_000
	m := ms / 60_000
	ms -= m * 60_000
	s := ms / 1000
	ms -= s * 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

func vttText() string {
	out := "WEBVTT\n\n"
	for i, e := range EventSeconds {
		out += fmt.Sprintf("cue-%d\n%s --> %s align:start\n%s\n\n", i+1, srtClock(e.Start), srtClock(e.End), e.Text)
	}
	return out
}

func chapterText() string {
	out := "{\n  \"version\": 1,\n  \"timebase\": \"1000/1\",\n  \"chapters\": [\n"
	for i, e := range EventSeconds {
		if i > 0 {
			out += ",\n"
		}
		out += fmt.Sprintf("    {\"title\": %q, \"start\": %q, \"end\": %q}", e.Text, srtClock(e.Start), srtClock(e.End))
	}
	out += "\n  ]\n}\n"
	return out
}

func legacyManifest() string {
	return `{
  "version": 1,
  "media_content_digest": "1111111111111111111111111111111111111111111111111111111111111111",
  "media_envelope_digest": "2222222222222222222222222222222222222222222222222222222222222222",
  "clips": [
    {"id": "clip-01", "event_id": "kickoff", "start_sec": 2.0, "end_sec": 4.0, "path": "clips/clip-01.mp4", "bytes": 1024, "sha256": "aaaa", "width": 1280, "height": 720, "fps": 30},
    {"id": "clip-02", "event_id": "counter", "start_sec": 6.0, "end_sec": 8.0, "path": "clips/clip-02.mp4", "bytes": 2048, "sha256": "bbbb", "width": 1280, "height": 720, "fps": 30}
  ]
}
`
}

// id3Time returns the MPEG-TS 90kHz presentation timestamp for a fixture
// event in seconds.
func id3Time(sec int64) uint64 { return uint64(sec * 90000) }

var _ = binary.BigEndian
var _ = metadata.KindID3
