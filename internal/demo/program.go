package demo

import (
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/audio"
	"mediad/internal/batch"
	"mediad/internal/clock"
	"mediad/internal/ingest"
	"mediad/internal/mederr"
)

// Program is the fully ingested, time-normalized media graph used by chunks.
type Program struct {
	Opt              Options
	Fingerprint      string
	Layout           audio.Layout
	EDL              *ingest.EDL
	Plan             []batch.PlanChunk
	Tracks           map[string]*audio.Buffer
	AlignmentErrorNs int64
	DriftSlope       float64
	VFRFrames        []clock.Frame
	Errors           mederr.List
}

// BuildProgram generates bounded fixtures, reads them through ingest, applies
// clock normalization and converts every track to the target audio format.
func BuildProgram(opt Options) (*Program, error) {
	if opt.Out == "" || opt.Chunk <= 0 || opt.TargetRate <= 0 {
		return nil, mederr.New(mederr.CatLimit, "demo", -1, -1, false, "invalid demo options")
	}
	layout, err := audio.ParseLayout(opt.LayoutName)
	if err != nil {
		return nil, err
	}
	fp, err := opt.Fingerprint()
	if err != nil {
		return nil, err
	}
	fixDir := filepath.Join(opt.Out, "fixtures")
	if err := os.MkdirAll(fixDir, 0o755); err != nil {
		return nil, mederr.New(mederr.CatIO, "demo", -1, -1, false, "create fixtures: %v", err)
	}

	mp4Path := filepath.Join(fixDir, "tracks.mp4")
	rtpPath := filepath.Join(fixDir, "rtp-frames.bin")
	edlPath := filepath.Join(fixDir, "timeline.edl.json")

	mono := GenerateTone(opt.Seed, 1, 22050, 1, 5)
	stereo44 := GenerateTone(opt.Seed, 2, 44100, 2, 5)
	rtpSrc := GenerateTone(opt.Seed, 0, 48000, 2, 5)
	if err := writeMP4Fixtures(mp4Path, mono, stereo44); err != nil {
		return nil, err
	}
	tracks, err := readMP4Tracks(mp4Path, opt, layout)
	if err != nil {
		return nil, err
	}

	rtpPCM, err := audio.EncodeS16LE(rtpSrc)
	if err != nil {
		return nil, err
	}
	if err := writeRTPFixture(rtpPath, rtpPCM, 48000, 2, opt.WrapAt, opt.DriftPPM); err != nil {
		return nil, err
	}
	rtpTrack, driftSlope, driftErrNs, err := readRTPTrack(rtpPath, opt)
	if err != nil {
		return nil, err
	}
	tracks["rtp:0"] = rtpTrack

	if _, err := writeEDLFixture(edlPath); err != nil {
		return nil, err
	}
	edlFile, err := os.Open(edlPath)
	if err != nil {
		return nil, mederr.New(mederr.CatIO, "edl", -1, -1, false, "open EDL fixture: %v", err)
	}
	parsedEDL, perr := ingest.ParseEDL(edlFile)
	edlFile.Close()
	if perr != nil {
		return nil, perr
	}
	if err := parsedEDL.Validate([]string{"mp4:1", "mp4:2", "rtp:0"}); err != nil {
		return nil, err
	}

	total := clock.Span{Start: 0, End: parsedEDL.TotalDuration()}
	plan, err := batch.PlanChunks(total, opt.Chunk.Nanoseconds())
	if err != nil {
		return nil, err
	}
	shortFP := fp
	if len(shortFP) > 12 {
		shortFP = shortFP[:12]
	}
	for i := range plan {
		plan[i].RelPath = filepath.ToSlash(filepath.Join("chunks",
			fmt.Sprintf("chunk-%03d-%s.wav", i, shortFP)))
	}

	boundaries := []int64{0, total.End}
	for _, s := range parsedEDL.Segments {
		boundaries = append(boundaries, s.StartNanos, s.End())
	}
	for _, c := range plan {
		boundaries = append(boundaries, c.Span.Start, c.Span.End)
	}
	alignNs := clock.MaxSnapErrorNs(boundaries, opt.TargetRate)
	if driftErrNs > alignNs {
		alignNs = driftErrNs
	}

	p := &Program{
		Opt:              opt,
		Fingerprint:      fp,
		Layout:           layout,
		EDL:              parsedEDL,
		Plan:             plan,
		Tracks:           tracks,
		AlignmentErrorNs: alignNs,
		DriftSlope:       driftSlope,
	}
	if opt.VFR {
		p.addVFR(total)
	}
	return p, nil
}

func writeMP4Fixtures(path string, mono, stereo44 *audio.Buffer) error {
	monoPCM, err := audio.EncodeS16LE(mono)
	if err != nil {
		return err
	}
	stereoPCM, err := audio.EncodeS16LE(stereo44)
	if err != nil {
		return err
	}
	return ingest.WriteFixtureMP4(path, []ingest.FixtureTrack{
		{ID: 1, Rate: mono.Rate, Channels: mono.Layout.Channels, PCM: monoPCM},
		{ID: 2, Rate: stereo44.Rate, Channels: stereo44.Layout.Channels, PCM: stereoPCM},
	})
}

func readMP4Tracks(path string, opt Options, targetLayout audio.Layout) (map[string]*audio.Buffer, error) {
	if _, err := ingest.ReadMP4Timeline(path); err != nil {
		return nil, err
	}
	out := map[string]*audio.Buffer{}
	for _, id := range []uint32{1, 2} {
		pcm, meta, err := ingest.ReadMP4PCMS16(path, id)
		if err != nil {
			return nil, err
		}
		layout := targetLayout
		if meta.Channels == 1 {
			layout = audio.LayoutMono
		} else if meta.Channels == 2 {
			layout = audio.LayoutStereo
		}
		native, err := audio.DecodeS16LE(pcm, meta.SampleRate, layout)
		if err != nil {
			return nil, err
		}
		ready, err := prepareTrack(native, opt, targetLayout)
		if err != nil {
			return nil, err
		}
		out[meta.Source] = ready
	}
	return out, nil
}

func readRTPTrack(path string, opt Options) (*audio.Buffer, float64, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, mederr.New(mederr.CatIO, "rtp:0", -1, -1, false, "open RTP fixture: %v", err)
	}
	defer f.Close()
	var parseErrs mederr.List
	stream, err := ingest.ReadRTPStream(f, 48000, "rtp:0", &parseErrs)
	if err != nil {
		return nil, 0, 0, err
	}
	var pcm []byte
	dc, err := clock.NewDriftCorrector(512)
	if err != nil {
		return nil, 0, 0, err
	}
	for _, pkt := range stream.Packets {
		pcm = append(pcm, pkt.Payload...)
		dc.Add(pkt.Extended, pkt.ArrivalNs)
	}
	var driftErr int64
	for _, pkt := range stream.Packets {
		corrected := dc.Correct(pkt.Extended)
		d := corrected - pkt.ArrivalNs
		if d < 0 {
			d = -d
		}
		if d > driftErr {
			driftErr = d
		}
	}
	slope, _ := dc.Slope()
	native, err := audio.DecodeS16LE(pcm, 48000, audio.LayoutStereo)
	if err != nil {
		return nil, 0, 0, err
	}
	ready, err := prepareTrack(native, opt, audio.LayoutStereo)
	return ready, slope, driftErr, err
}

func prepareTrack(in *audio.Buffer, opt Options, layout audio.Layout) (*audio.Buffer, error) {
	out, err := audio.Convert(in, opt.TargetRate, layout)
	if err != nil {
		return nil, err
	}
	if _, err := audio.NormalizeLoudness(out, opt.LoudnessTarget, opt.PeakCeilingDB); err != nil {
		return nil, err
	}
	return out, nil
}
