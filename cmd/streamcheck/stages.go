package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"streamcheck/core"
	"streamcheck/fixture"
	"streamcheck/manifest"
	"streamcheck/packet"
	"streamcheck/pipeline"
)

// Stage artifact shapes (serialized as JSON into checkpoints).
type jumpJSON struct {
	Rep    string `json:"rep"`
	Index  int    `json:"index"`
	WantMs int64  `json:"wantMs"`
	GotMs  int64  `json:"gotMs"`
}

type manifestArtifact struct {
	DashReps        int        `json:"dashReps"`
	DashSegments    int        `json:"dashSegments"`
	HLSSegments     int        `json:"hlsSegments"`
	Discontinuities int        `json:"discontinuities"`
	Intervals       int        `json:"intervals"`
	TimingJumps     []jumpJSON `json:"timingJumps"`
	CrossRep        string     `json:"crossRep"`
}

type packetArtifact struct {
	Received   int     `json:"received"`
	Delivered  int     `json:"delivered"`
	Duplicates int     `json:"duplicates"`
	Reordered  int     `json:"reordered"`
	Lost       int     `json:"lost"`
	MaxBurst   int     `json:"maxBurst"`
	Gaps       int     `json:"gaps"`
	MinKbps    float64 `json:"minKbps"`
	MaxKbps    float64 `json:"maxKbps"`
}

type inspectArtifact struct {
	CorruptSegment    int     `json:"corruptSegment"`
	CorruptOffset     int64   `json:"corruptOffset"`
	KeyframeTimesMs   []int64 `json:"keyframeTimesMs"`
	GOPMaxMs          int64   `json:"gopMaxMs"`
	GOPNormalMaxMs    int64   `json:"gopNormalMaxMs"`
	CrossRepKeyframes bool    `json:"crossRepKeyframes"`
	TruncatedError    string  `json:"truncatedError"`
	TruncatedOffset   int64   `json:"truncatedOffset"`
}

type migrateArtifact struct {
	RollbackChecked   bool   `json:"rollbackChecked"`
	RollbackIdentical bool   `json:"rollbackIdentical"`
	RecoveredFile     string `json:"recoveredFile"`
	RecoveredSHA256   string `json:"recoveredSha256"`
	RecoveredBytes    int    `json:"recoveredBytes"`
}

// buildEngine assembles the four-stage pipeline over a fixture work dir.
func buildEngine(work string, plan *fixture.Plan) *pipeline.Engine {
	return &pipeline.Engine{
		Work:   work,
		Inputs: plan.Inputs,
		Stages: []pipeline.Stage{
			manifestStage(work),
			packetStage(work),
			inspectStage(work),
			migrateStage(work, plan),
		},
	}
}

func readFixture(work, name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(work, name))
	if err != nil {
		return nil, core.E(core.KindInput, "read-fixture", "cannot read input", err).At(name, -1)
	}
	return data, nil
}

func marshalArtifact(v any) (string, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return "", core.E(core.KindVerify, "artifact", "cannot encode stage result", err)
	}
	return string(out), nil
}

func manifestStage(work string) pipeline.Stage {
	return pipeline.Stage{Name: "manifest", Run: func(map[string]string) (string, error) {
		mpdData, err := readFixture(work, fixture.MPDFile)
		if err != nil {
			return "", err
		}
		dash, err := manifest.ParseDASH(mpdData, fixture.MPDFile)
		if err != nil {
			return "", err
		}
		jumps := dash.Analyze()
		hlsData, err := readFixture(work, fixture.HLSFile)
		if err != nil {
			return "", err
		}
		hls, err := manifest.ParseHLS(hlsData, fixture.HLSFile)
		if err != nil {
			return "", err
		}
		jumps = append(jumps, hls.Analyze()...)
		art := manifestArtifact{
			DashReps:        len(dash.Reps),
			DashSegments:    len(dash.Reps[0].Segments),
			HLSSegments:     len(hls.Reps[0].Segments),
			Discontinuities: hls.Reps[0].Discontinuities(),
			Intervals:       hls.Reps[0].Intervals(),
			CrossRep:        "none",
		}
		if mis := manifest.CompareReps(dash.Reps[0], dash.Reps[1]); mis != nil {
			art.CrossRep = fmt.Sprintf("%s at segment %d", mis.Field, mis.Index)
		}
		for _, j := range jumps {
			art.TimingJumps = append(art.TimingJumps, jumpJSON{
				Rep: j.Rep, Index: j.Index, WantMs: j.Want.Millis(), GotMs: j.Got.Millis(),
			})
		}
		return marshalArtifact(art)
	}}
}

func packetStage(work string) pipeline.Stage {
	return pipeline.Stage{Name: "packet", Run: func(map[string]string) (string, error) {
		data, err := readFixture(work, fixture.CaptureFile)
		if err != nil {
			return "", err
		}
		re := packet.NewReorderer(fixture.RTPWindow)
		re.BucketMs = 100 // fine-grained sustained-bandwidth samples
		arrival := 0
		for pos := 0; pos < len(data); {
			if pos+2 > len(data) {
				return "", core.E(core.KindPacket, "read-capture", "truncated record header", nil).At(fixture.CaptureFile, int64(pos))
			}
			n := int(binary.BigEndian.Uint16(data[pos:]))
			pos += 2
			if pos+n > len(data) {
				return "", core.E(core.KindPacket, "read-capture", "truncated record", nil).At(fixture.CaptureFile, int64(pos))
			}
			rec, err := packet.Parse(data[pos:pos+n], arrival, int64(arrival)*fixture.RTPPacketMs)
			if err != nil {
				return "", err
			}
			re.Push(rec)
			arrival++
			pos += n
		}
		st := re.Flush()
		art := packetArtifact{
			Received: st.Received, Delivered: st.Delivered,
			Duplicates: st.Duplicates, Reordered: st.Reordered,
			Lost: st.Lost(), MaxBurst: st.MaxBurst(), Gaps: len(st.Gaps),
		}
		for i, b := range st.Bandwidth {
			if i == 0 || b.Kbps < art.MinKbps {
				art.MinKbps = b.Kbps
			}
			if i == 0 || b.Kbps > art.MaxKbps {
				art.MaxKbps = b.Kbps
			}
		}
		return marshalArtifact(art)
	}}
}
