package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"continuity/internal/damage"
	"continuity/internal/fixgen"
	"continuity/internal/ingest"
	"continuity/internal/manifest/provenance"
	mp4scan "continuity/internal/mp4"
	"continuity/internal/mp4/keyframe"
	"continuity/internal/pipeline"
	"continuity/internal/timeline"
)

// seekBuffer is an in-memory io.WriteSeeker for MP4 fixture synthesis.
type seekBuffer struct {
	buf []byte
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if grow := end - int64(len(s.buf)); grow > 0 {
		s.buf = append(s.buf, make([]byte, grow)...)
	}
	copy(s.buf[s.pos:end], p)
	s.pos = end
	return len(p), nil
}

func (s *seekBuffer) Seek(off int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case 0:
		base = 0
	case 1:
		base = s.pos
	case 2:
		base = int64(len(s.buf))
	}
	n := base + off
	if n < 0 {
		return 0, errors.New("negative seek")
	}
	s.pos = n
	return n, nil
}

func rtpParams(cfg config) fixgen.RTPParams {
	return fixgen.RTPParams{
		Seed: cfg.Seed, Count: 200, StartSeq: 65530, SamplesPerPkt: 960,
		PayloadSize:  int(cfg.AudioTarget / 400), // 20 ms packets
		LossEvery:    37,
		DupEvery:     29,
		ReorderEvery: 31,
		TruncateAt:   101,
		BadOpusAt:    149,
	}
}

func mp4Specs(cfg config) []fixgen.TrackSpec {
	rng := rand.New(rand.NewSource(cfg.Seed + 1))
	audioDeltas := make([]uint32, 188)
	audioSizes := make([]uint32, 188)
	for i := range audioDeltas {
		audioDeltas[i] = 1024
		audioSizes[i] = uint32(341 + rng.Intn(7) - 3)
	}
	audioDeltas[150] = 0 // anomalous timestamp
	audioSizes[100] = 0  // corrupt sample
	videoDeltas := make([]uint32, 100)
	videoSizes := make([]uint32, 100)
	keys := map[int]bool{0: true, 25: true, 75: true} // long GOP between 26 and 76
	for i := range videoDeltas {
		videoDeltas[i] = 3600
		if keys[i] {
			videoSizes[i] = 9000
		} else {
			videoSizes[i] = 3950
		}
	}
	videoDeltas[80] = 0 // anomalous timestamp
	videoSizes[60] = 0  // corrupt sample
	return []fixgen.TrackSpec{
		{ID: 1, Kind: "audio", Timescale: 48000, Deltas: audioDeltas, Sizes: audioSizes},
		{ID: 2, Kind: "video", Timescale: 90000, Deltas: videoDeltas, Sizes: videoSizes,
			Keyframes: []uint32{1, 26, 76}},
	}
}

func fixtureBytes(cfg config) (rtpBlob, mp4Blob []byte, err error) {
	var rtpBuf bytes.Buffer
	for _, dg := range fixgen.GenRTP(rtpParams(cfg)) {
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(dg)))
		rtpBuf.Write(n[:])
		rtpBuf.Write(dg)
	}
	sb := &seekBuffer{}
	if err := fixgen.WriteMP4(sb, mp4Specs(cfg)); err != nil {
		return nil, nil, err
	}
	return rtpBuf.Bytes(), sb.buf, nil
}

func inputDigest(cfg config) (string, error) {
	rtpBlob, mp4Blob, err := fixtureBytes(cfg)
	if err != nil {
		return "", err
	}
	return provenance.Combine(provenance.Sum(rtpBlob), provenance.Sum(mp4Blob)), nil
}

func configDigest(cfg config) string {
	b, _ := json.Marshal(struct {
		Seed, ReorderWindow, AudioTarget, VideoTarget, MaxKeyframeTicks int64
	}{cfg.Seed, int64(cfg.ReorderWindow), cfg.AudioTarget, cfg.VideoTarget, cfg.MaxKeyframeTicks})
	return provenance.Sum(b)
}

func readRTPFile(path string) ([][]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for len(b) > 0 {
		if len(b) < 4 {
			return nil, errors.New("rtp fixture: trailing partial length")
		}
		n := binary.LittleEndian.Uint32(b)
		b = b[4:]
		if uint32(len(b)) < n {
			return nil, errors.New("rtp fixture: truncated datagram record")
		}
		out = append(out, b[:n])
		b = b[n:]
	}
	return out, nil
}

func writeJSON(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func readJSON(path string, v interface{}) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// scanOut is the persisted result of the scan step.
type scanOut struct {
	RTP           *ingest.Result    `json:"rtp"`
	OpusFaults    []*timeline.Fault `json:"opusFaults"`
	MinSeq        uint64            `json:"minSeq"`
	SamplesPerPkt int64             `json:"samplesPerPkt"`
	ExpectedTicks int64             `json:"expectedTicks"`
	ReceivedBits  int64             `json:"receivedBits"`
	CoverageTicks int64             `json:"coverageTicks"`
	MP4           *mp4scan.File     `json:"mp4"`
}

// keyframeOut is the persisted result of the keyframe step.
type keyframeOut struct {
	Track     string              `json:"track"`
	Intervals []keyframe.Interval `json:"intervals"`
	Long      []timeline.Range    `json:"long"`
}

func node(id string, stage timeline.Stage, deps ...string) timeline.Node {
	return timeline.Node{ID: id, Stage: stage, Deps: deps}
}

func buildPipeline(cfg config) (*pipeline.Pipeline, error) {
	rtpPath := filepath.Join(cfg.Out, "input.rtp")
	mp4Path := filepath.Join(cfg.Out, "input.mp4")
	scanPath := filepath.Join(cfg.Out, "scan.json")
	damagePath := filepath.Join(cfg.Out, "damage.json")
	keyframePath := filepath.Join(cfg.Out, "keyframe.json")

	fixtures := func(ctx *pipeline.Context) error {
		rtpBlob, mp4Blob, err := fixtureBytes(cfg)
		if err != nil {
			return err
		}
		if err := os.WriteFile(rtpPath, rtpBlob, 0o644); err != nil {
			return err
		}
		return os.WriteFile(mp4Path, mp4Blob, 0o644)
	}

	scan := func(ctx *pipeline.Context) error {
		dgs, err := readRTPFile(rtpPath)
		if err != nil {
			return err
		}
		res := ingest.IngestRTP("audio/rtp", dgs, cfg.ReorderWindow)
		out := &scanOut{RTP: res, SamplesPerPkt: 960}
		for _, p := range res.Packets {
			out.ReceivedBits += int64(len(p.Payload)) * 8
			n, err := ingest.OpusPacketSamples("audio/rtp", p.Offset, p.Payload)
			if err != nil {
				var f *timeline.Fault
				if errors.As(err, &f) {
					f.Seq = int64(p.Seq)
					out.OpusFaults = append(out.OpusFaults, f)
				}
				continue // undecodable: reported, never fabricated
			}
			out.CoverageTicks += int64(n)
		}
		if len(res.Packets) > 0 {
			out.MinSeq = res.Packets[0].Seq
			maxSeq := res.Packets[len(res.Packets)-1].Seq
			out.ExpectedTicks = int64(maxSeq-out.MinSeq+1) * out.SamplesPerPkt
		}
		mf, err := mp4scan.ScanFile(mp4Path)
		if err != nil {
			return err
		}
		out.MP4 = mf
		return writeJSON(scanPath, out)
	}

	damageStep := func(ctx *pipeline.Context) error {
		var s scanOut
		if err := readJSON(scanPath, &s); err != nil {
			return err
		}
		var ranges []timeline.Range
		for _, g := range s.RTP.Gaps {
			ranges = append(ranges, timeline.Range{
				Track: "audio/rtp", Reason: "packet gap",
				StartSeq: int64(g.StartSeq), EndSeq: int64(g.EndSeq),
				Start: timeline.Ticks((int64(g.StartSeq) - int64(s.MinSeq)) * s.SamplesPerPkt),
				End:   timeline.Ticks((int64(g.EndSeq) - int64(s.MinSeq) + 1) * s.SamplesPerPkt),
			})
		}
		for _, f := range s.RTP.Faults {
			ranges = append(ranges, timeline.Range{
				Track: f.Track, Reason: "truncated RTP datagram",
				StartOffset: f.Offset, EndOffset: f.Offset,
			})
		}
		for _, f := range s.OpusFaults {
			r := timeline.Range{
				Track: f.Track, Reason: "undecodable Opus packet",
				StartSeq: f.Seq, EndSeq: f.Seq,
				StartOffset: f.Offset, EndOffset: f.Offset,
			}
			if f.Seq >= int64(s.MinSeq) {
				r.Start = timeline.Ticks((f.Seq - int64(s.MinSeq)) * s.SamplesPerPkt)
				r.End = r.Start + timeline.Ticks(s.SamplesPerPkt)
			}
			ranges = append(ranges, r)
		}
		for _, tr := range s.MP4.Tracks {
			for _, f := range tr.Faults {
				r := timeline.Range{
					Track: f.Track, Reason: f.Msg,
					StartOffset: f.Offset, EndOffset: f.Offset,
				}
				if f.Offset >= 0 && int(f.Offset) < len(tr.Samples) {
					sm := tr.Samples[f.Offset]
					r.Start, r.End = sm.Start, sm.Start+sm.Duration
				}
				ranges = append(ranges, r)
			}
		}
		return writeJSON(damagePath, damage.Merge(ranges))
	}

	keyframeStep := func(ctx *pipeline.Context) error {
		var s scanOut
		if err := readJSON(scanPath, &s); err != nil {
			return err
		}
		var outs []keyframeOut
		for _, tr := range s.MP4.Tracks {
			if tr.Kind != "video" {
				continue
			}
			outs = append(outs, keyframeOut{
				Track:     fmt.Sprintf("%s/mp4#%d", tr.Kind, tr.ID),
				Intervals: keyframe.Intervals(tr),
				Long:      keyframe.FlagLong(tr, timeline.Ticks(cfg.MaxKeyframeTicks)),
			})
		}
		return writeJSON(keyframePath, outs)
	}

	manifestStep := func(ctx *pipeline.Context) error {
		return buildManifest(cfg, scanPath, damagePath, keyframePath, rtpPath, mp4Path)
	}

	return pipeline.New(
		&pipeline.Step{Node: node("fixtures", timeline.StageIngest), Version: 1, Fn: fixtures},
		&pipeline.Step{Node: node("scan", timeline.StageScan, "fixtures"), Version: 1, Fn: scan},
		&pipeline.Step{Node: node("damage", timeline.StageDamage, "scan"), Version: 1, Fn: damageStep},
		&pipeline.Step{Node: node("keyframe", timeline.StageKeyframe, "scan"), Version: 1, Fn: keyframeStep},
		&pipeline.Step{Node: node("manifest", timeline.StageManifest, "damage", "keyframe"), Version: 1, Fn: manifestStep},
	)
}
