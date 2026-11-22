package fixtures_test

import (
	"strings"
	"testing"

	"github.com/pairwise/mediad/internal/fixtures"
	"github.com/pairwise/mediad/internal/mp4"
)

func sampleSpec() fixtures.FileSpec {
	return fixtures.FileSpec{Timescale: 1000, MoovFirst: true, Brands: []string{"isom", "qt  "},
		BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []fixtures.TrackSpec{
			{ID: 1, Codec: fixtures.CodecAVC, Timescale: 90000, SampleDur: 3000,
				Samples: 90, KeyEvery: 9, SampleSize: 96, SampleSeed: 7},
			{ID: 2, Codec: fixtures.CodecPCM, Timescale: 48000, SampleDur: 960,
				Samples: 90, KeyEvery: 1, SampleSize: 2, SampleSeed: 8,
				Silence: []fixtures.Range{{Start: 9600, End: 19200}}},
		}}
}

func TestDamageInjectionProducesLocatableFaults(t *testing.T) {
	base := fixtures.Build(sampleSpec())
	cases := []fixtures.DamageKind{fixtures.DamageTruncatedBox, fixtures.DamageSampleSize, fixtures.DamageClockWrap, fixtures.DamageMissingSync}
	for _, kind := range cases {
		raw, err := fixtures.InjectDamage(base, kind)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		tree := mp4.Scan("mem", bytesAt(raw), int64(len(raw)))
		if tree.First("moov") == nil {
			t.Fatalf("%s: damaged file lost moov", kind)
		}
		movie, err := mp4.ParseMovie(tree)
		if len(movie.Tracks) == 0 {
			t.Fatalf("%s: no tracks survive", kind)
		}
		if kind == fixtures.DamageSampleSize || kind == fixtures.DamageMissingSync {
			if err == nil {
				t.Fatalf("%s: expected recoverable parse error", kind)
			}
		}
	}
}

func TestTruncatedBoxCutsInsideMdat(t *testing.T) {
	base := fixtures.Build(sampleSpec())
	raw, err := fixtures.InjectDamage(base, fixtures.DamageTruncatedBox)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= len(base) {
		t.Fatal("truncation did not shorten the file")
	}
	tree := mp4.Scan("mem", bytesAt(raw), int64(len(raw)))
	mdat := tree.First("mdat")
	if mdat == nil || !mdat.Truncated {
		t.Fatalf("mdat not marked truncated: %+v", mdat)
	}
	if mdat.PayloadSize <= 0 {
		t.Fatal("no readable mdat payload remains")
	}
}

func TestClockWrapKeepsPrefixReadable(t *testing.T) {
	base := fixtures.Build(sampleSpec())
	raw, err := fixtures.InjectDamage(base, fixtures.DamageClockWrap)
	if err != nil {
		t.Fatal(err)
	}
	tree := mp4.Scan("mem", bytesAt(raw), int64(len(raw)))
	movie, perr := mp4.ParseMovie(tree)
	if perr != nil {
		// stts rewriting preserves the sample count, so no parse error.
		t.Fatalf("unexpected parse error: %v", perr)
	}
	tr := movie.Tracks[0]
	if len(tr.Samples) != 90 {
		t.Fatalf("samples=%d", len(tr.Samples))
	}
	if tr.Samples[0].DTS != 0 || tr.Samples[1].DTS != 3000 {
		t.Fatalf("prefix dts %d,%d", tr.Samples[0].DTS, tr.Samples[1].DTS)
	}
	if tr.Samples[2].DTS != 3000+0x80000003 {
		t.Fatalf("post-wrap dts %d", tr.Samples[2].DTS)
	}
}

func TestMissingSyncShrinksBoxesConsistently(t *testing.T) {
	base := fixtures.Build(sampleSpec())
	raw, err := fixtures.InjectDamage(base, fixtures.DamageMissingSync)
	if err != nil {
		t.Fatal(err)
	}
	tree := mp4.Scan("mem", bytesAt(raw), int64(len(raw)))
	for _, w := range tree.Warnings {
		if strings.Contains(w.Detail, "implausible") || w.Kind == "truncated" && !strings.Contains(w.Detail, "stss") {
			t.Fatalf("box hierarchy inconsistent: %v", w)
		}
	}
	stss := tree.First("stss")
	if stss == nil {
		t.Fatal("stss gone")
	}
	if stss.Size != 16+6*4 {
		t.Fatalf("stss size=%d", stss.Size)
	}
}

func TestPCMFixtureRoundTripsThroughParser(t *testing.T) {
	raw := fixtures.Build(sampleSpec())
	tree := mp4.Scan("mem", bytesAt(raw), int64(len(raw)))
	movie, err := mp4.ParseMovie(tree)
	if err != nil {
		t.Fatal(err)
	}
	var audio *mp4.Track
	for _, tr := range movie.Tracks {
		if tr.IsAudio() {
			audio = tr
		}
	}
	if audio == nil {
		t.Fatal("no audio track")
	}
	if audio.SampleRate != 48000 || audio.Channels != 2 {
		t.Fatalf("audio entry: %d Hz %d ch", audio.SampleRate, audio.Channels)
	}
}

type bytesAt []byte

func (b bytesAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(b)) {
		return 0, errEOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, errEOF
	}
	return n, nil
}

var errEOF = &eofErr{}

type eofErr struct{}

func (*eofErr) Error() string { return "eof" }
