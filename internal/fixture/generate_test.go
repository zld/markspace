package fixture

import (
	"path/filepath"
	"testing"

	"replaykit/media/rtp"
)

func TestProofEventFileHasThreeEvents(t *testing.T) {
	ef := ProofEventFile()
	if len(ef.Events) != 3 {
		t.Fatalf("events=%d want 3", len(ef.Events))
	}
	for _, e := range ef.Events {
		if e.AtNS <= 0 || e.PreNS <= 0 || e.PostNS <= 0 {
			t.Fatalf("event %+v must carry positive times", e)
		}
	}
}

func TestWriteSampleTreeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	written, err := WriteSampleTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) < 7 {
		t.Fatalf("written=%d want >=7 files", len(written))
	}
	ef, err := LoadEvents(filepath.Join(dir, "events.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ef.Events) != 3 {
		t.Fatalf("loaded events=%d want 3", len(ef.Events))
	}
}

func TestLossDupFixtureShape(t *testing.T) {
	st := LossDup()
	pkts, _, err := rtp.ParseFramed(st.Framed, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	res := rtp.Order(pkts, rtp.DefaultOptions(st.Params.SampleRate))
	if len(res.Lost) != 1 || len(res.Duplicates) != 1 {
		t.Fatalf("lost=%d dup=%d want 1/1", len(res.Lost), len(res.Duplicates))
	}
}

func TestCleanStreamsHaveNoDefects(t *testing.T) {
	for _, st := range []AudioStream{OpusClean(20), AACClean(20)} {
		if err := st.Params.Validate(); err != nil {
			t.Fatalf("%s params: %v", st.Name, err)
		}
		pkts, anom, err := rtp.ParseFramed(st.Framed, 1<<16)
		if err != nil || len(anom) != 0 {
			t.Fatalf("%s parse err=%v anom=%d", st.Name, err, len(anom))
		}
		res := rtp.Order(pkts, rtp.DefaultOptions(st.Params.SampleRate))
		if len(res.Lost)+len(res.Duplicates)+len(res.Late) != 0 {
			t.Fatalf("%s defects lost=%d dup=%d late=%d", st.Name,
				len(res.Lost), len(res.Duplicates), len(res.Late))
		}
	}
}
