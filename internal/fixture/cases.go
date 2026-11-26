package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Case names accepted by the regress command.
const (
	CaseAll       = "all"
	CaseClean     = "clean"
	CaseTruncated = "truncated"
	CaseCorrupt   = "corrupt"
	CaseNoIndex   = "no-index"
	CaseClockGap  = "clock-gap"
)

// Cases lists every generated case.
var Cases = []string{CaseClean, CaseTruncated, CaseCorrupt, CaseNoIndex, CaseClockGap}

// ValidCase reports whether name is a known case (or "all").
func ValidCase(name string) bool {
	if name == CaseAll {
		return true
	}
	for _, c := range Cases {
		if c == name {
			return true
		}
	}
	return false
}

// Layout constants shared by the generator and the checker.
const (
	MasterName        = "master.m3u8"
	MediaName         = "index.m3u8"
	IFrameName        = "iframes.m3u8"
	InitName          = "init.mp4"
	SegmentPattern    = "seg%d.m4s"
	WAVPattern        = "seg%d.wav"
	VariantAudioGroup = "aud"
	// SegmentsPerLayer is the required three segments per representation.
	SegmentsPerLayer = 3
)

// Layer describes one representation of the generated stream.
type Layer struct {
	Name       string
	Kind       string // "video" or "audio"
	Path       string
	Channels   int
	SampleRate int
	Bits       int
}

// DefaultLayers is the required shape: two bitrate variants with a video and a
// three-channel 24-bit audio rendition each, three segments per layer.
func DefaultLayers() []Layer {
	return []Layer{
		{Name: "v0", Kind: "video", Path: "v0"},
		{Name: "v1", Kind: "video", Path: "v1"},
		{Name: "a1", Kind: "audio", Path: "a1", Channels: 3, SampleRate: 48000, Bits: 24},
		{Name: "a2", Kind: "audio", Path: "a2", Channels: 3, SampleRate: 48000, Bits: 24},
	}
}

// audioLayerFor maps each video variant to its audio rendition.
var audioLayerFor = map[string]string{"v0": "a1", "v1": "a2"}

// VideoDurations90k are the per-segment video durations in 90 kHz ticks.
var VideoDurations90k = []uint32{270000, 270000, 240000}

// AudioDurationsMs are the per-segment audio durations in milliseconds.
var AudioDurationsMs = []int{3000, 3000, 2500}

// SegmentRecord is one generated media segment's authoritative facts.
type SegmentRecord struct {
	Layer          string
	Index          int // zero-based
	Path           string
	Bytes          []byte
	InitBytes      []byte
	Timescale      uint32
	Layout         []SampleLoc
	KeyframeRanges []ByteRange // relative to the segment file
	SourceWAV      []byte      // audio only: the exact PCM source file
	DeclaredDur    Rational
}

// Rational is an exact duration used for declared playlists.
type Rational struct{ Num, Den int64 }

// Float gives a decimal for playlist text.
func (r Rational) Float() float64 { return float64(r.Num) / float64(r.Den) }

// Tree is the generated fixture layout for one case.
type Tree struct {
	Root     string
	Case     string
	Layers   []Layer
	Segments map[string][]SegmentRecord // layer -> segments
}

// Generate writes fixture trees into outDir. For "all" every case is written
// as its own subdirectory; otherwise just the requested case.
func Generate(outDir, caseName string) (*Tree, error) {
	if !ValidCase(caseName) {
		return nil, fmt.Errorf("unknown case %q", caseName)
	}
	if caseName == CaseAll {
		var last *Tree
		for _, c := range Cases {
			t, err := GenerateOne(outDir, c)
			if err != nil {
				return nil, err
			}
			last = t
		}
		return last, nil
	}
	return GenerateOne(outDir, caseName)
}

// GenerateOne builds a single case tree at outDir/<case>/.
func GenerateOne(outDir, caseName string) (*Tree, error) {
	root := filepath.Join(outDir, caseName)
	if err := os.RemoveAll(root); err != nil {
		return nil, err
	}
	t := &Tree{Root: root, Case: caseName, Layers: DefaultLayers(),
		Segments: map[string][]SegmentRecord{}}
	if err := t.build(); err != nil {
		return nil, err
	}
	if err := applyCaseFault(root, caseName); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Tree) build() error {
	for _, l := range t.Layers {
		dir := filepath.Join(t.Root, filepath.FromSlash(l.Path))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		var recs []SegmentRecord
		var err error
		switch l.Kind {
		case "video":
			recs, err = buildVideo(l)
		case "audio":
			recs, err = buildAudio(l)
		}
		if err != nil {
			return err
		}
		t.Segments[l.Name] = recs
		for _, r := range recs {
			if err := os.WriteFile(filepath.Join(t.Root, filepath.FromSlash(r.Path)), r.Bytes, 0o644); err != nil {
				return err
			}
			if r.SourceWAV != nil {
				wavPath := filepath.Join(t.Root, filepath.FromSlash(l.Path), fmt.Sprintf(WAVPattern, r.Index+1))
				if err := os.WriteFile(wavPath, r.SourceWAV, 0o644); err != nil {
					return err
				}
			}
			initPath := filepath.Join(t.Root, filepath.FromSlash(l.Path), InitName)
			if err := os.WriteFile(initPath, r.InitBytes, 0o644); err != nil {
				return err
			}
		}
	}
	return t.writeManifests()
}

func absoluteKeyframes(layout []SampleLoc) []ByteRange {
	var out []ByteRange
	for _, loc := range layout {
		if loc.Keyframe {
			out = append(out, ByteRange{Offset: loc.Offset, Length: int64(loc.Size)})
		}
	}
	return out
}

func buildVideo(l Layer) ([]SegmentRecord, error) {
	initBytes, err := BuildInit([]TrackSpec{{TrackID: 1, Timescale: 90000, Handler: "vide",
		Codec: "avc1", Width: 640, Height: 360}})
	if err != nil {
		return nil, err
	}
	var recs []SegmentRecord
	var decodeTime uint64
	for i, dur := range VideoDurations90k {
		n := int(dur / 3000)
		if n < 1 {
			n = 1
		}
		var samples []SampleSpec
		for k := 0; k < n; k++ {
			key := k == 0
			au := AnnexBAU(key, 300+20*i+k, i*10+k)
			samples = append(samples, SampleSpec{Duration: 3000, Size: uint32(len(au)),
				Keyframe: key, Payload: au})
		}
		seg, layout, err := BuildSegmentLayout([]FragmentSpec{{Sequence: uint32(i + 1),
			BaseDecodeTime: decodeTime, TrackID: 1, Samples: samples}})
		if err != nil {
			return nil, err
		}
		decodeTime += uint64(dur)
		recs = append(recs, SegmentRecord{
			Layer: l.Name, Index: i, Path: l.Path + "/" + fmt.Sprintf(SegmentPattern, i+1),
			Bytes: seg, InitBytes: initBytes, Timescale: 90000, Layout: layout.Samples,
			KeyframeRanges: absoluteKeyframes(layout.Samples),
			DeclaredDur:    Rational{Num: int64(dur), Den: 90000},
		})
	}
	return recs, nil
}

// BlockFrames is the number of PCM frames per audio sample.
const BlockFrames = 1024

func buildAudio(l Layer) ([]SegmentRecord, error) {
	initBytes, err := BuildInit([]TrackSpec{{TrackID: 1, Timescale: uint32(l.SampleRate),
		Handler: "soun", Codec: "ipcm", Channels: l.Channels, SampleSize: l.Bits,
		SampleRate: l.SampleRate}})
	if err != nil {
		return nil, err
	}
	var recs []SegmentRecord
	var decodeTime uint64
	for i, ms := range AudioDurationsMs {
		frames := l.SampleRate * ms / 1000
		wav, err := BuildWAV(WAVSpec{Channels: l.Channels, Bits: l.Bits,
			SampleRate: uint32(l.SampleRate), Frames: frames, Seed: seedFor(l.Name)})
		if err != nil {
			return nil, err
		}
		var samples []SampleSpec
		for k := 0; k < frames; k += BlockFrames {
			n := BlockFrames
			if k+n > frames {
				n = frames - k
			}
			start := int(wav.DataOffset) + k*wav.BlockAlign
			payload := wav.Bytes[start : start+n*wav.BlockAlign]
			samples = append(samples, SampleSpec{Duration: uint32(n),
				Size: uint32(len(payload)), Keyframe: true, Payload: payload})
		}
		seg, layout, err := BuildSegmentLayout([]FragmentSpec{{Sequence: uint32(i + 1),
			BaseDecodeTime: decodeTime, TrackID: 1, Samples: samples}})
		if err != nil {
			return nil, err
		}
		decodeTime += uint64(frames)
		recs = append(recs, SegmentRecord{
			Layer: l.Name, Index: i, Path: l.Path + "/" + fmt.Sprintf(SegmentPattern, i+1),
			Bytes: seg, InitBytes: initBytes, Timescale: uint32(l.SampleRate),
			Layout: layout.Samples, KeyframeRanges: absoluteKeyframes(layout.Samples),
			SourceWAV: wav.Bytes, DeclaredDur: Rational{Num: int64(ms), Den: 1000},
		})
	}
	return recs, nil
}

func seedFor(name string) int {
	sum := sha256.Sum256([]byte(name))
	return int(sum[0])
}

func (t *Tree) writeManifests() error {
	// The master references the two video variants and declares a single
	// audio group carrying both three-channel renditions.
	var master strings.Builder
	master.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n")
	for _, name := range []string{"a1", "a2"} {
		l := t.layer(name)
		master.WriteString(fmt.Sprintf(
			"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=%q,NAME=%q,LANGUAGE=%q,CHANNELS=%q,DEFAULT=YES,URI=%q\n",
			VariantAudioGroup, "Audio "+name, "en", fmt.Sprint(l.Channels), l.Path+"/"+MediaName))
	}
	variants := []struct {
		video, audio string
		bw           int
		res          string
	}{
		{"v0", "a1", 1280000, "640x360"},
		{"v1", "a2", 2560000, "1280x720"},
	}
	for _, v := range variants {
		master.WriteString(fmt.Sprintf(
			"#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=%q,RESOLUTION=%s,AUDIO=%q\n",
			v.bw, "avc1.4d401f,mp4a.40.2", v.res, VariantAudioGroup))
		master.WriteString(v.video + "/" + MediaName + "\n")
	}
	if err := os.WriteFile(filepath.Join(t.Root, MasterName), []byte(master.String()), 0o644); err != nil {
		return err
	}
	for _, l := range t.Layers {
		if err := t.writeMediaPlaylist(l); err != nil {
			return err
		}
		if l.Kind == "video" {
			if err := t.writeIFramePlaylist(l); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Tree) layer(name string) Layer {
	for _, l := range t.Layers {
		if l.Name == name {
			return l
		}
	}
	return Layer{}
}

func (t *Tree) writeMediaPlaylist(l Layer) error {
	recs := t.Segments[l.Name]
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n")
	sb.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=%q\n", InitName))
	for _, r := range recs {
		sb.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n", r.DeclaredDur.Float()))
		sb.WriteString(fmt.Sprintf(SegmentPattern+"\n", r.Index+1))
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	return os.WriteFile(filepath.Join(t.Root, filepath.FromSlash(l.Path), MediaName), []byte(sb.String()), 0o644)
}

// writeIFramePlaylist emits one byte-range entry per keyframe sample, in
// segment order. Ranges are relative to the referenced segment file.
func (t *Tree) writeIFramePlaylist(l Layer) error {
	recs := t.Segments[l.Name]
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:4\n#EXT-X-I-FRAMES-ONLY\n")
	sb.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	sb.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=%q\n", InitName))
	for _, r := range recs {
		for _, br := range r.KeyframeRanges {
			sb.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n", r.DeclaredDur.Float()))
			sb.WriteString(fmt.Sprintf("#EXT-X-BYTERANGE:%d@%d\n", br.Length, br.Offset))
			sb.WriteString(fmt.Sprintf(SegmentPattern+"\n", r.Index+1))
		}
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	return os.WriteFile(filepath.Join(t.Root, filepath.FromSlash(l.Path), IFrameName), []byte(sb.String()), 0o644)
}

// ByteRange is a byte extent within one file.
type ByteRange struct {
	Offset int64
	Length int64
}

// applyCaseFault injects the deliberate defect for a case.
func applyCaseFault(root, caseName string) error {
	switch caseName {
	case CaseClean:
		return nil
	case CaseTruncated:
		// Cut the last audio segment mid-sample and a video segment inside its
		// mdat, leaving the declared playlist dangling.
		if err := truncateFile(filepath.Join(root, "a1", "seg3.m4s"), 700); err != nil {
			return err
		}
		return truncateFile(filepath.Join(root, "v0", "seg2.m4s"), 120)
	case CaseCorrupt:
		// Corrupt fragment metadata of a2/seg2.m4s; only that layer stops.
		return corruptFragment(filepath.Join(root, "a2", "seg2.m4s"))
	case CaseNoIndex:
		for _, l := range DefaultLayers() {
			if l.Kind != "video" {
				continue
			}
			p := filepath.Join(root, l.Path, IFrameName)
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	case CaseClockGap:
		// v1's playlist declares 3.5 s for its first segment while the media
		// fragment says 3.0 s: a reproducible gap on v1 only.
		return injectClockGap(filepath.Join(root, "v1", MediaName))
	}
	return fmt.Errorf("unknown case %q", caseName)
}

func truncateFile(path string, n int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if n >= len(data) {
		return fmt.Errorf("truncation %d exceeds file %s (%d bytes)", n, path, len(data))
	}
	return os.WriteFile(path, data[:len(data)-n], 0o644)
}

func corruptFragment(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Overwrite the moof box type with a corrupt fourcc; the box size then
	// points at unparsable content.
	idx := bytesIndex(data, []byte("moof"))
	if idx < 0 {
		return fmt.Errorf("no moof in %s", path)
	}
	data[idx] ^= 0xFF
	return os.WriteFile(path, data, 0o644)
}

func bytesIndex(hay, needle []byte) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func injectClockGap(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := strings.Replace(string(data), "#EXTINF:3.000000,", "#EXTINF:3.500000,", 1)
	if s == string(data) {
		return fmt.Errorf("clock-gap injection found no target EXTINF")
	}
	return os.WriteFile(path, []byte(s), 0o644)
}

// Digest returns a hex SHA-256 of a byte slice.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
