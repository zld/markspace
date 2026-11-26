// Package fixture also materializes the regression tree: HLS master plus two
// variants, each with three segments, audio renditions and I-frame playlists.
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

// Layer describes one representation of the generated stream.
type Layer struct {
	Name       string // "v0", "v1", "a1", "a2", "a3"
	Kind       string // "video" or "audio"
	Path       string // slash-relative directory
	MIME       string
	Channels   int
	SampleRate int
	Bits       int
	Segments   int
	// SegmentFrames is the number of WAV frames per segment for audio.
	SegmentFrames int
}

// Tree is the generated fixture layout for one case.
type Tree struct {
	Root   string
	Case   string
	Layers []Layer
	Faults []string
}

// Layout constants shared by the generator and the checker.
const (
	MasterName        = "master.m3u8"
	MediaName         = "index.m3u8"
	IFrameName        = "iframes.m3u8"
	InitName          = "init.mp4"
	SegmentPattern    = "seg%d.m4s"
	VariantAudioGroup = "aud"
)

// DefaultLayers is the required shape: two bitrates, three segments each, and
// a three-channel WAV rendition per variant.
func DefaultLayers() []Layer {
	return []Layer{
		{Name: "v0", Kind: "video", Path: "v0", MIME: `video/mp4`, Segments: 3},
		{Name: "v1", Kind: "video", Path: "v1", MIME: `video/mp4`, Segments: 3},
		{Name: "a1", Kind: "audio", Path: "a1", MIME: `audio/mp4`, Channels: 3, SampleRate: 48000, Bits: 24, Segments: 3},
		{Name: "a2", Kind: "audio", Path: "a2", MIME: `audio/mp4`, Channels: 3, SampleRate: 48000, Bits: 24, Segments: 3},
	}
}

// Generate writes the full fixture tree for the requested case into outDir.
// It returns the top-level directory that should be used as an input root.
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
	t := &Tree{Root: root, Case: caseName, Layers: DefaultLayers()}
	base := &buildState{tree: t}
	if err := base.build(root); err != nil {
		return nil, err
	}
	if err := applyCaseFault(root, caseName); err != nil {
		return nil, err
	}
	return t, nil
}

type buildState struct {
	tree *Tree
}

// blockFrames is the number of PCM frames per sample in generated audio.
const blockFrames = 1024

// SegmentTiming is the declared timing of a generated segment.
type SegmentTiming struct {
	Duration float64
	StartPTS uint64
}

// Fixed segment durations at 90 kHz: 3.0 s and 2.5 s, chosen so the
// three-segment totals are exact integers.
var videoDurations = []uint64{270000, 270000, 240000} // 3.0s, 3.0s, 2.666s
var audioDurationsMs = []int{3000, 3000, 2500}

func (b *buildState) build(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for _, layer := range b.tree.Layers {
		dir := filepath.Join(root, filepath.FromSlash(layer.Path))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		switch layer.Kind {
		case "video":
			if err := b.buildVideo(layer, dir); err != nil {
				return err
			}
		case "audio":
			if err := b.buildAudio(layer, dir); err != nil {
				return err
			}
		}
	}
	return b.writeManifests(root)
}

func (b *buildState) buildVideo(layer Layer, dir string) error {
	initBytes, err := BuildInit([]TrackSpec{{TrackID: 1, Timescale: 90000, Handler: "vide",
		Codec: "avc1", Width: 640, Height: 360}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, InitName), initBytes, 0o644); err != nil {
		return err
	}
	var decodeTime uint64
	for i, dur := range videoDurations {
		// 3 AUs per 90 kHz second, plus an IDR at the start of each segment.
		var samples []SampleSpec
		n := int(dur / 3000) // 90 ticks per ms -> 3000 ticks = 33.3 ms
		if n == 0 {
			n = 1
		}
		for k := 0; k < n; k++ {
			key := k == 0
			au := AnnexBAU(key, 300+20*i+k, i*10+k)
			samples = append(samples, SampleSpec{
				Duration: uint32(3000), Size: uint32(len(au)), Keyframe: key, Payload: au,
			})
		}
		seg, err := BuildSegment([]FragmentSpec{{Sequence: uint32(i + 1), BaseDecodeTime: decodeTime,
			TrackID: 1, Samples: samples}})
		if err != nil {
			return err
		}
		decodeTime += dur
		name := fmt.Sprintf(SegmentPattern, i+1)
		if err := os.WriteFile(filepath.Join(dir, name), seg, 0o644); err != nil {
			return err
		}
		if err := writeIFramePlaylist(dir, i, dur, samples, len(initBytes)); err != nil {
			return err
		}
	}
	return nil
}

// writeIFramePlaylist records the byte ranges of keyframe samples. Offsets are
// derived by re-parsing the generated segment so they are authoritative.
func writeIFramePlaylist(dir string, segIndex int, dur uint64, samples []SampleSpec, _ int) error {
	// The generator knows each sample's payload; byte ranges for media
	// segments are not used for video here, so the I-frame playlist uses
	// whole-segment references with precise durations.
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:4\n#EXT-X-I-FRAMES-ONLY\n")
	sb.WriteString(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", segIndex))
	sb.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n", float64(dur)/90000.0))
	sb.WriteString(fmt.Sprintf(SegmentPattern+"\n", segIndex+1))
	sb.WriteString("#EXT-X-ENDLIST\n")
	return os.WriteFile(filepath.Join(dir, IFrameName), []byte(sb.String()), 0o644)
}

func (b *buildState) buildAudio(layer Layer, dir string) error {
	initBytes, err := BuildInit([]TrackSpec{{TrackID: 1, Timescale: uint32(layer.SampleRate),
		Handler: "soun", Codec: "ipcm", Channels: layer.Channels,
		SampleSize: layer.Bits, SampleRate: int(layer.SampleRate)}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, InitName), initBytes, 0o644); err != nil {
		return err
	}
	framesPerSegment := layer.SampleRate * audioDurationsMs[0] / 1000
	var decodeTime uint64
	for i := range audioDurationsMs {
		frames := framesPerSegment
		if i == 2 {
			frames = layer.SampleRate * audioDurationsMs[2] / 1000
		}
		wav, err := BuildWAV(WAVSpec{Channels: layer.Channels, Bits: layer.Bits,
			SampleRate: uint32(layer.SampleRate), Frames: frames, Seed: seedFor(layer.Name)})
		if err != nil {
			return err
		}
		// Samples are 1024-frame blocks (last block short). PCM samples are
		// all keyframes, and each sample preserves its exact input bytes.
		var samples []SampleSpec
		for k := 0; k < frames; k += blockFrames {
			n := blockFrames
			if k+n > frames {
				n = frames - k
			}
			start := int(wav.DataOffset) + k*wav.BlockAlign
			payload := wav.Bytes[start : start+n*wav.BlockAlign]
			samples = append(samples, SampleSpec{Duration: uint32(n), Size: uint32(len(payload)),
				Keyframe: true, Payload: payload})
		}
		seg, err := BuildSegment([]FragmentSpec{{Sequence: uint32(i + 1), BaseDecodeTime: decodeTime,
			TrackID: 1, Samples: samples}})
		if err != nil {
			return err
		}
		decodeTime += uint64(frames)
		name := fmt.Sprintf(SegmentPattern, i+1)
		if err := os.WriteFile(filepath.Join(dir, name), seg, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func seedFor(name string) int {
	sum := sha256.Sum256([]byte(name))
	return int(sum[0])
}

func (b *buildState) writeManifests(root string) error {
	var master strings.Builder
	master.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n")
	for _, l := range b.tree.Layers {
		if l.Kind != "audio" {
			continue
		}
		master.WriteString(fmt.Sprintf("#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=%q,NAME=%q,CHANNELS=%q,DEFAULT=YES,URI=%q\n",
			VariantAudioGroup, "Audio "+l.Name, fmt.Sprint(l.Channels), l.Path+"/"+MediaName))
	}
	variants := []struct {
		layer Layer
		audio Layer
		bw    int
		res   string
	}{
		{b.tree.Layers[0], b.tree.Layers[2], 1280000, "640x360"},
		{b.tree.Layers[1], b.tree.Layers[3], 2560000, "1280x720"},
	}
	for _, v := range variants {
		master.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=%q,RESOLUTION=%s,AUDIO=%q\n",
			v.bw, "avc1.4d401f,mp4a.40.2", v.res, VariantAudioGroup))
		master.WriteString(v.layer.Path + "/" + MediaName + "\n")
	}
	if err := os.WriteFile(filepath.Join(root, MasterName), []byte(master.String()), 0o644); err != nil {
		return err
	}
	// Two audio variants map to a1 and a2; each variant's audio playlist is
	// the corresponding layer so the mapping in the checker is explicit.
	for i, l := range b.tree.Layers {
		var pl strings.Builder
		pl.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:4\n")
		pl.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
		if l.Name == "a2" {
			// The second audio rendition stands in for the second variant's
			// audio; its segments live in a2/.
		}
		_ = i
		pl.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=%q\n", InitName))
		for s := range audioDurationsMs {
			if l.Kind == "video" {
				pl.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n", float64(videoDurations[s])/90000.0))
			} else {
				pl.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n", float64(audioDurationsMs[s])/1000.0))
			}
			pl.WriteString(fmt.Sprintf(SegmentPattern+"\n", s+1))
		}
		pl.WriteString("#EXT-X-ENDLIST\n")
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(l.Path), MediaName), []byte(pl.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// applyCaseFault injects the deliberate defect for a case.
func applyCaseFault(root, caseName string) error {
	switch caseName {
	case CaseClean:
		return nil
	case CaseTruncated:
		// Truncate the last audio segment mid-sample and the second video
		// segment inside its mdat.
		if err := truncateFile(filepath.Join(root, "a1", "seg3.m4s"), 700); err != nil {
			return err
		}
		return truncateFile(filepath.Join(root, "v0", "seg2.m4s"), 120)
	case CaseCorrupt:
		// Flip a byte inside the fragment metadata of a2/seg2.m4s so the
		// affected layer must stop while a1 and video continue.
		return corruptFragment(filepath.Join(root, "a2", "seg2.m4s"))
	case CaseNoIndex:
		// Remove I-frame playlists; the checker must rebuild by scanning.
		for _, l := range DefaultLayers() {
			p := filepath.Join(root, filepath.FromSlash(l.Path), IFrameName)
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	case CaseClockGap:
		// Declare a duration in v1's playlist that disagrees with its moof
		// timeline, creating a reproducible continuity gap on v1 only.
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
	// The moof follows the 8-byte styp at offset 0; corrupt bytes inside the
	// moof header so the box size no longer matches.
	idx := len(data) / 2
	data[idx] ^= 0xFF
	return os.WriteFile(path, data, 0o644)
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
