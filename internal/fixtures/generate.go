package fixtures

import (
	"fmt"
	"os"
	"path/filepath"

	"rtx/internal/cmaf"
	"rtx/internal/rtp"
)

// Dataset names.
const (
	NameGood      = "good"
	NameEdge      = "edge"
	NameGap       = "gap"
	NameRollback  = "rollback"
	NameSwitch    = "switch"
	NameTruncated = "truncated"
	NameBadCache  = "badcache"
	NameOversize  = "oversize"
	NameQueue     = "queue"
	NameNoMFHD    = "nomfhd"
)

// Scenario describes one generated dataset.
type Scenario struct {
	Name     string
	Dir      string
	Manifest string
}

// Options tune generation.
type Options struct {
	FramesPerFragment int
	FragmentCount     int
}

// DefaultOptions produce 3 frames per fragment and 4 fragments per source, so
// the two-source edge dataset has eight records, well inside the queue limit.
func DefaultOptions() Options {
	return Options{FramesPerFragment: 3, FragmentCount: 4}
}

// Generate writes every fixture dataset under workDir.
func Generate(workDir string, opts Options) ([]Scenario, error) {
	if opts.FramesPerFragment <= 0 || opts.FragmentCount <= 0 {
		return nil, fmt.Errorf("fixtures: frames and fragment count must be positive")
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("fixtures: create %s: %w", workDir, err)
	}
	builders := []func(string, Options) (Scenario, error){
		buildGood, buildEdge, buildGapOnly, buildRollbackOnly, buildSwitchOnly,
		buildTruncated, buildBadCache, buildOversize, buildQueueOverflow, buildNoMFHD,
	}
	var out []Scenario
	for _, b := range builders {
		sc, err := b(workDir, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

// sourcePlan describes how one source's fragments are laid out. Mutations are
// opt-in so each scenario states exactly which anomaly it demonstrates.
type sourcePlan struct {
	id         string
	ssrc       uint32
	rtpBase    uint32
	tsBase     uint32
	mediaBase  int64
	seqGapAt   int
	rollbackAt int
	omitMFHDAt int
}

func noMutation() sourcePlan { return sourcePlan{seqGapAt: -1, rollbackAt: -1, omitMFHDAt: -1} }

// generateDataset writes init segments, fragments, RTP traces and a manifest
// into its own directory.
func generateDataset(workDir string, opts Options, name string, sources []sourcePlan, extraEvents []cmaf.Event, truncate bool) (Scenario, error) {
	dir := filepath.Join(workDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Scenario{}, fmt.Errorf("fixtures: create %s: %w", dir, err)
	}
	initSpec := cmaf.InitSpec{
		VideoTimescale: VideoTimescale,
		AudioTimescale: AudioTimescale,
		Width:          Width,
		Height:         Height,
		SampleRate:     SampleRate,
		Channels:       Channels,
		MajorBrand:     [4]byte{'i', 's', 'o', 'm'},
	}
	initBytes, err := cmaf.BuildInit(initSpec)
	if err != nil {
		return Scenario{}, err
	}
	manifest := &cmaf.Manifest{Version: 1}
	manifest.MediaRate.Num = MediaRateNum
	manifest.MediaRate.Den = MediaRateDen

	for _, src := range sources {
		s, events, err := generateSource(dir, opts, src, initBytes)
		if err != nil {
			return Scenario{}, err
		}
		manifest.Sources = append(manifest.Sources, s)
		manifest.Events = append(manifest.Events, events...)
	}
	manifest.Events = append(manifest.Events, extraEvents...)
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := cmaf.WriteManifestFile(manifestPath, manifest); err != nil {
		return Scenario{}, err
	}
	if truncate {
		if err := truncateLastFragment(manifestPath, dir); err != nil {
			return Scenario{}, err
		}
	}
	return Scenario{Name: name, Dir: dir, Manifest: manifestPath}, nil
}

// generateSource writes one source's files and returns its manifest entry plus
// the fragment events describing it.
func generateSource(dir string, opts Options, src sourcePlan, initBytes []byte) (cmaf.Source, []cmaf.Event, error) {
	initName := src.id + "-init.mp4"
	if err := writeAtomic(filepath.Join(dir, initName), initBytes); err != nil {
		return cmaf.Source{}, nil, err
	}
	entry := cmaf.Source{ID: src.id, Init: initName, MediaOffset: src.mediaBase}
	var events []cmaf.Event
	var packets []rtp.Packet
	seq := uint16(1)
	ts := src.tsBase
	var videoTFD, audioTFD uint64
	mfhd := uint32(1)
	videoIndex, audioSample := 0, 0

	for f := 0; f < opts.FragmentCount; f++ {
		var vs, as []cmaf.MuxSample
		for k := 0; k < opts.FramesPerFragment; k++ {
			vs = append(vs, cmaf.MuxSample{Data: JPEGFrame(videoIndex), Duration: VideoTicksPerFrame, Key: true})
			videoIndex++
			pcm := PCMFrame(audioSample, AudioTicksPerFrame)
			audioSample += AudioTicksPerFrame
			as = append(as, cmaf.MuxSample{Data: pcm, Duration: AudioTicksPerFrame})
			// Two RTP packets per audio frame, 10 ms each.
			half := (AudioTicksPerFrame / 2) * 2
			for j := 0; j < 2; j++ {
				p, err := rtp.BuildPacket(src.id, src.ssrc, 111, seq, ts, false, pcm[j*half:(j+1)*half])
				if err != nil {
					return cmaf.Source{}, nil, fmt.Errorf("fixtures: build rtp: %w", err)
				}
				packets = append(packets, p)
				seq++
				ts += uint32(AudioTicksPerFrame / 2)
			}
		}
		// A rollback affects only the emitted tfdt; the correct running
		// timeline continues underneath so exactly one fragment rewinds.
		emitVideo, emitAudio := videoTFD, audioTFD
		if src.rollbackAt == f {
			emitVideo /= 2
			emitAudio /= 2
		}
		omit := src.omitMFHDAt == f
		seqNum := mfhd
		data, err := cmaf.BuildFragment(cmaf.FragmentSpec{
			Sequence: seqNum, OmitMFHD: omit, VideoTFD: emitVideo, AudioTFD: emitAudio, Video: vs, Audio: as,
		})
		if err != nil {
			return cmaf.Source{}, nil, fmt.Errorf("fixtures: build fragment: %w", err)
		}
		fragName := fmt.Sprintf("%s-%04d.m4s", src.id, f)
		if err := writeAtomic(filepath.Join(dir, fragName), data); err != nil {
			return cmaf.Source{}, nil, err
		}
		entry.Fragments = append(entry.Fragments, cmaf.FragmentRef{Path: fragName, Order: f + 1})
		ev := cmaf.Event{Kind: cmaf.EventFragment, Source: src.id, Path: fragName, Order: f + 1, MediaTicks: src.mediaBase + int64(videoTFD)*8}
		if !omit {
			ev.MFHD = u32ptr(mfhd)
		}
		events = append(events, ev)
		videoTFD += uint64(opts.FramesPerFragment * VideoTicksPerFrame)
		audioTFD += uint64(opts.FramesPerFragment * AudioTicksPerFrame)
		mfhd++
		if src.seqGapAt == f {
			// One mfhd number is absent from the input. Its media is genuinely
			// missing, so no duration is added to the running timeline; the
			// next present fragment simply continues from where media resumes.
			mfhd++
			events = append(events, cmaf.Event{Kind: cmaf.EventLoss, Source: src.id, Note: "mfhd gap"})
		}
	}
	traceName := src.id + ".rtp.jsonl"
	if err := rtp.WriteTraceFile(dir, traceName, packets); err != nil {
		return cmaf.Source{}, nil, fmt.Errorf("fixtures: write trace: %w", err)
	}
	entry.RTPTrace = traceName
	entry.RTPAnchors = []cmaf.RTPAnchor{{
		Source: src.id, RTPTimestamp: src.tsBase, Wrap: 0, MediaTicks: src.mediaBase, ClockRate: AudioTimescale,
	}}
	return entry, events, nil
}

func u32ptr(v uint32) *uint32 { return &v }

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".fx-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// truncateLastFragment cuts the final fragment mid-mdat so the parser hits a
// declared size larger than the remaining bytes.
func truncateLastFragment(manifestPath, dir string) error {
	m, err := cmaf.LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	var last string
	for _, s := range m.Sources {
		for _, f := range s.Fragments {
			last = f.Path
		}
	}
	path := filepath.Join(dir, last)
	full, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(full) < 32 {
		return fmt.Errorf("fixtures: fragment %s too short to truncate", last)
	}
	return os.WriteFile(path, full[:len(full)-13], 0o644)
}
