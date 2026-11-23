package fixture

import (
	"bytes"
	"encoding/json"
	"testing"

	"mediadist/internal/codec"
)

func TestGenerateIsDeterministic(t *testing.T) {
	a, err := Generate(DefaultSpec())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := Generate(DefaultSpec())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !bytes.Equal(a.IndexJSON, b.IndexJSON) {
		t.Fatal("index bytes differ between identical seeds")
	}
	if len(a.Streams) != len(b.Streams) {
		t.Fatalf("stream count %d vs %d", len(a.Streams), len(b.Streams))
	}
	for i := range a.Streams {
		if len(a.Streams[i].Datagrams) != len(b.Streams[i].Datagrams) {
			t.Fatalf("stream %d datagram count differs", i)
		}
		for j := range a.Streams[i].Datagrams {
			if !bytes.Equal(a.Streams[i].Datagrams[j], b.Streams[i].Datagrams[j]) {
				t.Fatalf("stream %d datagram %d differs", i, j)
			}
		}
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	// Begin from the defaults so sample counts and keyframe spacing are valid;
	// only the seed varies.
	sa, sb := DefaultSpec(), DefaultSpec()
	sa.Seed, sb.Seed = 1, 2
	a, err := Generate(sa)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(sb)
	if err != nil {
		t.Fatal(err)
	}
	// The index addresses structural offsets, which the seed does not move;
	// what must differ is the media bytes themselves.
	// Payload bytes must differ even at the same structural positions.
	same := 0
	for i := range a.Streams[0].AccessUnits {
		if bytes.Equal(a.Streams[0].AccessUnits[i].Data, b.Streams[0].AccessUnits[i].Data) {
			same++
		}
	}
	if same == len(a.Streams[0].AccessUnits) {
		t.Fatal("media bytes ignored the seed")
	}
	if len(a.Streams[0].Datagrams) == 0 {
		t.Fatal("seed change produced no datagrams")
	}
	differing := 0
	for i := range a.Streams[0].Datagrams {
		if i < len(b.Streams[0].Datagrams) &&
			!bytes.Equal(a.Streams[0].Datagrams[i], b.Streams[0].Datagrams[i]) {
			differing++
		}
	}
	if differing == 0 {
		t.Fatal("datagrams ignored the seed")
	}
}

func TestAccessUnitsScanAsIntended(t *testing.T) {
	fx, err := Generate(DefaultSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range fx.Streams {
		for _, u := range st.AccessUnits {
			var res codec.ScanResult
			switch st.Codec {
			case "h264":
				res = codec.ScanH264(0, u.Data, codec.DefaultScanLimits())
			case "h265":
				res = codec.ScanH265(0, u.Data, codec.DefaultScanLimits())
			case "av1":
				res = codec.ScanAV1(0, u.Data, codec.DefaultScanLimits())
			}
			if len(res.Damaged) != 0 {
				t.Fatalf("%s sample %d clean bytes reported damage %+v", st.Codec, u.Index, res.Damaged)
			}
			found := len(res.Keyframes) > 0
			if found != u.Keyframe {
				t.Fatalf("%s sample %d keyframe=%t but scan found %t",
					st.Codec, u.Index, u.Keyframe, found)
			}
			if u.Keyframe && u.KeyUnitOffset < 0 {
				t.Fatalf("%s keyframe sample %d has no unit offset", st.Codec, u.Index)
			}
		}
	}
}

func TestFaultCountsMatchSummary(t *testing.T) {
	fx, err := Generate(DefaultSpec())
	if err != nil {
		t.Fatal(err)
	}
	f := fx.Faults
	if f.Duplicates != 2 {
		t.Fatalf("duplicates %d", f.Duplicates)
	}
	if f.Reordered != 4 {
		t.Fatalf("reordered %d", f.Reordered)
	}
	if f.LossBursts != 2 || f.LostPackets != 4 {
		t.Fatalf("loss bursts %d packets %d", f.LossBursts, f.LostPackets)
	}
	if f.Repairs != 2 {
		t.Fatalf("repairs %d", f.Repairs)
	}
	if f.Truncated != 1 {
		t.Fatalf("truncated %d", f.Truncated)
	}
	if f.BadIndex != 1 {
		t.Fatalf("bad index %d", f.BadIndex)
	}
	// The repair map must contain exactly the repaired sequences and no others.
	totalRepairs := 0
	for _, st := range fx.Streams {
		totalRepairs += len(st.Repairs)
	}
	if totalRepairs != f.Repairs {
		t.Fatalf("repair map has %d entries, summary says %d", totalRepairs, f.Repairs)
	}
}

func TestIndexPointsAtKeyframeUnits(t *testing.T) {
	fx, err := Generate(DefaultSpec())
	if err != nil {
		t.Fatal(err)
	}
	var rows struct {
		Entries []struct {
			Stream    int    `json:"stream"`
			Sample    int    `json:"sample"`
			Timestamp uint32 `json:"timestamp"`
			Offset    int64  `json:"offset"`
			Keyframe  bool   `json:"keyframe"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(fx.IndexJSON, &rows); err != nil {
		t.Fatalf("index is not valid JSON: %v", err)
	}
	if len(rows.Entries) != 10 {
		t.Fatalf("index has %d rows want 10", len(rows.Entries))
	}
	byTS := map[uint32]*AccessUnit{}
	for _, u := range fx.Streams[0].AccessUnits {
		byTS[u.Timestamp] = &AccessUnit{
			Index: u.Index, Keyframe: u.Keyframe, Data: u.Data, KeyUnitOffset: u.KeyUnitOffset,
			Timestamp: u.Timestamp,
		}
	}
	for _, row := range rows.Entries {
		u, ok := byTS[row.Timestamp]
		if !ok {
			t.Fatalf("index row points at unknown timestamp %d", row.Timestamp)
		}
		if !u.Keyframe {
			t.Fatalf("index row for non-key sample %d", u.Index)
		}
		if row.Offset != u.KeyUnitOffset {
			t.Fatalf("index offset %d want %d for sample %d", row.Offset, u.KeyUnitOffset, u.Index)
		}
	}
}

func TestRejectsImpossibleFaultParameters(t *testing.T) {
	cases := []Spec{
		{SampleCount: 2, KeyframeEvery: 10}, // too few keyframes
		{LossBurst: 1},                      // below the minimum
		{LossBurst: 99},                     // above packetization width
		{TruncatedSample: 500},              // outside the stream
	}
	for _, spec := range cases {
		if _, err := Generate(spec); err == nil {
			t.Fatalf("spec %+v should be rejected", spec)
		}
	}
}
