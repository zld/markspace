package fixture

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"medtrace/internal/ebml"
	"medtrace/internal/mederr"
	"medtrace/internal/rtp"
	"medtrace/internal/timeline"
	"medtrace/internal/track"

	pionrtp "github.com/pion/rtp"
)

// parseWebM walks the generated stream and counts SimpleBlocks per track.
func parseWebM(t *testing.T, data []byte) map[uint64]int {
	t.Helper()
	r := ebml.NewReader(bytes.NewReader(data))
	counts := map[uint64]int{}
	err := r.WalkChildren(nil, func(top *ebml.Element) error {
		if top.ID != ebml.IDSegment {
			return r.SeekTo(top.End())
		}
		if err := r.SeekTo(top.BodyOffset); err != nil {
			return err
		}
		return r.WalkChildren(top, func(segChild *ebml.Element) error {
			if segChild.ID != ebml.IDCluster {
				return r.SeekTo(segChild.End())
			}
			if err := r.SeekTo(segChild.BodyOffset); err != nil {
				return err
			}
			return r.WalkChildren(segChild, func(clChild *ebml.Element) error {
				if clChild.ID == ebml.IDSimpleBlock {
					body, err := r.ReadBody(clChild)
					if err != nil {
						return err
					}
					blk, err := ebml.ParseBlock(body)
					if err != nil {
						return err
					}
					counts[blk.TrackNum]++
				}
				return r.SeekTo(clChild.End())
			})
		})
	})
	if err != nil {
		t.Fatalf("parse webm: %v", err)
	}
	return counts
}

func TestGenerateValidWebM(t *testing.T) {
	data, refs := GenerateWebM(DefaultWebMConfig(7))
	if len(data) == 0 {
		t.Fatal("empty webm")
	}
	counts := parseWebM(t, data)
	if counts[1] == 0 || counts[2] != 2*counts[1] {
		t.Fatalf("block counts wrong: %+v", counts)
	}
	if len(refs) != counts[1]+counts[2] {
		t.Fatalf("refs=%d blocks=%d", len(refs), counts[1]+counts[2])
	}
}

func TestUnknownLengthTruncationDetected(t *testing.T) {
	cfg := DefaultWebMConfig(7)
	cfg.Corruption = CorruptUnknownLen
	data, _ := GenerateWebM(cfg)
	r := ebml.NewReader(bytes.NewReader(data))
	// The cut lands inside the final cluster; a full recursive walk must
	// surface it as a structured truncation error rather than silently
	// stopping, because an unknown-length segment's only legal terminator
	// is a clean parent boundary or a clean EOF.
	var walkErr error
	walkErr = r.WalkChildren(nil, func(top *ebml.Element) error {
		if top.ID != ebml.IDSegment || !top.UnknownSize {
			return r.SeekTo(top.End())
		}
		if err := r.SeekTo(top.BodyOffset); err != nil {
			return err
		}
		return r.WalkChildren(top, func(c *ebml.Element) error {
			if c.ID != ebml.IDCluster {
				return r.SeekTo(c.End())
			}
			if err := r.SeekTo(c.BodyOffset); err != nil {
				return err
			}
			return r.WalkChildren(c, func(leaf *ebml.Element) error {
				if leaf.ID == ebml.IDSimpleBlock {
					if _, err := r.ReadBody(leaf); err != nil {
						return err
					}
				}
				return r.SeekTo(leaf.End())
			})
		})
	})
	se := mederr.AsError(walkErr)
	if se == nil {
		t.Fatal("truncated unknown-length stream must produce structured error")
	}
	if se.Category != mederr.CatTruncatedHeader && se.Category != mederr.CatLengthOutOfBounds {
		t.Fatalf("want truncation/length error, got %v", se)
	}
	if se.Offset < 0 {
		t.Fatalf("error must carry byte offset: %v", se)
	}
}

func TestInvalidVINTDetected(t *testing.T) {
	cfg := DefaultWebMConfig(7)
	cfg.Corruption = CorruptInvalidVINT
	data, _ := GenerateWebM(cfg)
	r := ebml.NewReader(bytes.NewReader(data))
	err := r.WalkChildren(nil, func(top *ebml.Element) error {
		if top.ID != ebml.IDSegment {
			return r.SeekTo(top.End())
		}
		if err := r.SeekTo(top.BodyOffset); err != nil {
			return err
		}
		return r.WalkChildren(top, func(c *ebml.Element) error { return r.SeekTo(c.End()) })
	})
	se := mederr.AsError(err)
	if se == nil || se.Category != mederr.CatInvalidVINT {
		t.Fatalf("want invalid_vint with offset and path, got %v", se)
	}
	if se.Offset < 0 || !strings.Contains(se.Error(), "Segment") {
		t.Fatalf("error must carry offset and element path: %v", se)
	}
}

func TestEventsMatchAnalyzerTruth(t *testing.T) {
	cfg := DefaultEventConfig(7)
	recs, truth := GenerateEvents(cfg)
	if len(recs) == 0 {
		t.Fatal("no events generated")
	}
	a, err := rtp.NewAnalyzer(cfg.LateWindow)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		raw, err := hex.DecodeString(rec.RawHex)
		if err != nil {
			t.Fatal(err)
		}
		pkt := &pionrtp.Packet{}
		if err := pkt.Unmarshal(raw); err != nil {
			t.Fatal(err)
		}
		if err := a.AddPacket(timeline.Timestamp(rec.ArrivalNs), pkt); err != nil {
			t.Fatal(err)
		}
	}
	stats := a.Flush()
	if len(stats) != 1 {
		t.Fatalf("want one SSRC, got %d", len(stats))
	}
	st := stats[0]
	if st.Recovered != int64(truth.Recovered) {
		t.Errorf("recovered=%d want %d", st.Recovered, truth.Recovered)
	}
	if st.Lost != int64(truth.PermanentLoss) {
		t.Errorf("lost=%d want %d", st.Lost, truth.PermanentLoss)
	}
	if st.Duplicates != int64(truth.Duplicates) {
		t.Errorf("duplicates=%d want %d", st.Duplicates, truth.Duplicates)
	}
	if st.Reordered != int64(truth.Reordered) {
		t.Errorf("reordered=%d want %d", st.Reordered, truth.Reordered)
	}
	rate := st.RecoveryRate()
	if rate < 0.8 || rate > 1.0 {
		t.Errorf("recovery rate=%f outside [0.8,1.0]", rate)
	}
}

func TestCycleFixtureDetected(t *testing.T) {
	specs := CycleTracks()
	var tracks2 []track.Track
	for _, s := range specs {
		tracks2 = append(tracks2, track.Track{
			ID: s.ID, CodecID: s.Codec, Kind: track.Kind(s.Kind), DependsOn: s.DependsOn,
		})
	}
	g, err := track.NewGraph(tracks2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.TopoSort(); mederr.AsError(err) == nil ||
		mederr.AsError(err).Category != mederr.CatCycle {
		t.Fatalf("cycle fixture must trigger dependency_cycle, got %v", err)
	}
}

func TestMP4HelperRoundTrip(t *testing.T) {
	seen, err := ProbeMP4()
	if err != nil {
		t.Fatalf("go-mp4 helper failed: %v", err)
	}
	if len(seen) != 1 || seen[0] != "free" {
		t.Fatalf("go-mp4 box walk = %v, want [free]", seen)
	}
}

func TestDeterministicGeneration(t *testing.T) {
	a, _ := GenerateWebM(DefaultWebMConfig(7))
	b, _ := GenerateWebM(DefaultWebMConfig(7))
	if !bytes.Equal(a, b) {
		t.Fatal("webm generation must be deterministic per seed")
	}
	ea, _ := GenerateEvents(DefaultEventConfig(7))
	eb, _ := GenerateEvents(DefaultEventConfig(7))
	if !bytes.Equal(MarshalEvents(ea), MarshalEvents(eb)) {
		t.Fatal("event generation must be deterministic per seed")
	}
}
