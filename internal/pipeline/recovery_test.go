package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/fixtures"
)

// damageBase returns a clean generated file for injection tests.
func damageBase() fixtures.FileSpec {
	return fixtures.FileSpec{Timescale: 1000, MoovFirst: true, Brands: []string{"isom", "qt  "},
		BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []fixtures.TrackSpec{
			{ID: 1, Codec: fixtures.CodecAVC, Timescale: 90000, SampleDur: 3000,
				Samples: 180, KeyEvery: 18, SampleSize: 96, SampleSeed: 11},
			{ID: 2, Codec: fixtures.CodecPCM, Timescale: 48000, SampleDur: 960,
				Samples: 180, KeyEvery: 1, SampleSize: 2, SampleSeed: 12,
				Silence: []fixtures.Range{{Start: 48000, End: 96000}}},
		}}
}

func writeDamaged(t *testing.T, kind fixtures.DamageKind) string {
	t.Helper()
	raw, err := fixtures.InjectDamage(fixtures.Build(damageBase()), kind)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), string(kind)+".mp4")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTruncatedInputKeepsVerifiedSegments(t *testing.T) {
	sess, err := OpenSession(writeDamaged(t, fixtures.DamageTruncatedBox))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.Err == nil {
		t.Fatal("truncation not reported")
	}
	if fault.ExitCode(sess.Err) != 1 {
		t.Fatalf("exit=%d", fault.ExitCode(sess.Err))
	}
	planner, err := sess.Planner()
	if err != nil {
		t.Fatal(err)
	}
	res, _ := planner.Run(sess.Requests())
	if len(res.Segments) == 0 {
		t.Fatal("no segments recovered")
	}
	for _, seg := range res.Segments {
		if !seg.Recovered {
			t.Fatalf("damaged-input segment not marked recovered: %+v", seg)
		}
		if seg.SampleCount <= 0 || seg.SHA256 == "" {
			t.Fatalf("incomplete segment: %+v", seg)
		}
	}
}

func TestCorruptSampleSizeExcludesBadSample(t *testing.T) {
	sess, err := OpenSession(writeDamaged(t, fixtures.DamageSampleSize))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var video *bool
	for _, tr := range sess.Movie.Tracks {
		if tr.IsAudio() {
			continue
		}
		ok := true
		for _, s := range tr.Samples {
			if s.Index == 7 && s.InMdat {
				ok = false
			}
		}
		video = &ok
	}
	if video == nil || !*video {
		t.Fatal("corrupt sample was retained")
	}
	planner, _ := sess.Planner()
	res, _ := planner.Run(sess.Requests())
	if len(res.Segments) == 0 {
		t.Fatal("no segment produced")
	}
	src := NewSampleSource(sess.Tree, sess.Movie)
	for _, seg := range res.Segments {
		if _, err := src.ReadSample(seg.TrackID, seg.FirstSample); err != nil {
			t.Fatalf("segment anchor unreadable: %v", err)
		}
	}
}

func TestClockWrapWithholdsPostWrapSamples(t *testing.T) {
	sess, err := OpenSession(writeDamaged(t, fixtures.DamageClockWrap))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.Err == nil {
		t.Fatal("wrap not reported")
	}
	if !isWrap(sess.Err) {
		t.Fatalf("wrong cause: %v", sess.Err)
	}
	// Presentation stops at the wrap, so the index must only cover the prefix.
	for _, ti := range sess.Index.Tracks {
		if len(ti.Entries) == 0 {
			continue
		}
		last := ti.Entries[len(ti.Entries)-1]
		if last.Sample > 5 {
			t.Fatalf("post-wrap sample %d entered the index", last.Sample)
		}
	}
	planner, err := sess.Planner()
	if err != nil {
		t.Fatal(err)
	}
	res, _ := planner.Run(sess.Requests())
	if len(res.Segments) == 0 {
		t.Fatal("prefix not delivered")
	}
	for _, seg := range res.Segments {
		if seg.FirstSample+seg.SampleCount > 8 {
			t.Fatalf("segment reaches post-wrap samples: %+v", seg)
		}
	}
}

func isWrap(err error) bool {
	e, ok := fault.As(err)
	if !ok {
		return false
	}
	for e != nil {
		if e.Err == fault.ErrTimeWrap {
			return true
		}
		next, ok := fault.As(e.Err)
		if !ok {
			break
		}
		e = next
	}
	return false
}

func TestMissingSyncFramesFallBackToInference(t *testing.T) {
	sess, err := OpenSession(writeDamaged(t, fixtures.DamageMissingSync))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.Err == nil {
		t.Fatal("sync gap not reported")
	}
	planner, err := sess.Planner()
	if err != nil {
		t.Fatal(err)
	}
	res, _ := planner.Run(sess.Requests())
	if len(res.Segments) == 0 {
		t.Fatal("no segment")
	}
	if res.KeyframeHits != res.Cuts {
		t.Fatalf("inferred cuts counted as hits: %d/%d", res.KeyframeHits, res.Cuts)
	}
}
