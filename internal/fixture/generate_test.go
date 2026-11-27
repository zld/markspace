package fixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"example.com/assetpack/internal/fault"
	"example.com/assetpack/internal/ts"
)

func TestBuildTSIsDeterministic(t *testing.T) {
	spec := AssetSpec{Name: "intro", Rate: 44100, Channels: 2, Frames: 4410}
	a := BuildTS(StreamOptions{Asset: spec})
	b := BuildTS(StreamOptions{Asset: spec})
	if !bytes.Equal(a, b) {
		t.Fatal("generator is not deterministic")
	}
	if len(a)%ts.PacketSize != 0 {
		t.Fatalf("stream length %d is not packet aligned", len(a))
	}
}

func TestBuildTSParses(t *testing.T) {
	spec := AssetSpec{Name: "intro", Rate: 44100, Channels: 2, Frames: 4410}
	data := BuildTS(StreamOptions{Asset: spec})
	s := ts.Parse(data)
	if s.Stats.SyncErrors != 0 || s.Stats.TruncatedBytes != 0 || s.Stats.CCJumps != 0 || s.Stats.CCDuplicates != 0 {
		t.Fatalf("clean fixture produced faults: %+v", s.Stats)
	}
	psi := ts.ParsePSI(s)
	if psi.Current == nil {
		t.Fatalf("no PMT: %v", psi.Faults.Items)
	}
	if a, ok := psi.Current.AudioStream(); !ok || a.PID != 0x102 {
		t.Fatalf("audio stream not advertised: %+v", psi.Current.Streams)
	}
	el := ts.DemuxAudio(s, 0x102, spec.Channels*2)
	if el.Bytes() == 0 {
		t.Fatal("no audio recovered")
	}
	if el.SampleCount(spec.Channels*2) != 4410 {
		t.Fatalf("recovered %d frames, want 4410", el.SampleCount(spec.Channels*2))
	}
}

func TestBuildTSWithPCRWrap(t *testing.T) {
	spec := AssetSpec{Name: "bed", Rate: 48000, Channels: 1, Frames: 48000}
	data := BuildTS(StreamOptions{Asset: spec, WrapPCR: true})
	s := ts.Parse(data)
	if s.Stats.PCRWraps != 1 {
		t.Fatalf("expected exactly one PCR wrap, got %d (%v)", s.Stats.PCRWraps, s.Faults.Items)
	}
	var zeroSeen bool
	for _, p := range s.Packets {
		if !p.HasPCR {
			continue
		}
		if p.PCRRaw == 0 {
			zeroSeen = true
		}
	}
	if !zeroSeen {
		t.Fatal("no raw PCR at the modulus boundary was exercised")
	}
}

func TestBrokenFaultsAreLocated(t *testing.T) {
	spec := AssetSpec{Name: "sting", Rate: 48000, Channels: 2, Frames: 24000}
	fs, err := ParseFaultSet("truncated-ts,cc-jump,psi-update")
	if err != nil {
		t.Fatal(err)
	}
	if got := fs.Names(); len(got) != 3 {
		t.Fatalf("fault set names %v", got)
	}
	broken := BuildBroken(StreamOptions{Asset: spec}, fs)
	for _, name := range []string{"cc-jump", "psi-update", "truncated-ts"} {
		if _, ok := broken.Offsets[name]; !ok {
			t.Fatalf("offset for %s not recorded", name)
		}
	}
	s := ts.Parse(broken.Data)
	if findKind(s.Faults, fault.KindCCJump) == nil {
		t.Fatalf("cc-jump not detected: %v", s.Faults.Items)
	}
	if findKind(s.Faults, fault.KindTruncated) == nil {
		t.Fatalf("truncation not detected: %v", s.Faults.Items)
	}
	psi := ts.ParsePSI(s)
	var sawUpdate bool
	for _, ev := range psi.Events {
		if ev.Table == "PMT" && ev.To == 1 {
			sawUpdate = true
			if ev.Offset < broken.Offsets["psi-update"] {
				t.Fatalf("PSI event offset %d precedes insertion point %d", ev.Offset, broken.Offsets["psi-update"])
			}
		}
	}
	if !sawUpdate {
		t.Fatalf("psi-update not observed: %+v", psi.Events)
	}
}

func TestBrokenCCDuplicateAndPCRRegress(t *testing.T) {
	spec := AssetSpec{Name: "bed", Rate: 48000, Channels: 1, Frames: 24000}
	fs, err := ParseFaultSet("cc-duplicate,pcr-regress")
	if err != nil {
		t.Fatal(err)
	}
	broken := BuildBroken(StreamOptions{Asset: spec}, fs)
	s := ts.Parse(broken.Data)
	if findKind(s.Faults, fault.KindCCDuplicate) == nil {
		t.Fatalf("cc-duplicate not detected: %v", s.Faults.Items)
	}
	if findKind(s.Faults, fault.KindPCR) == nil {
		t.Fatalf("pcr regression not detected: %v", s.Faults.Items)
	}
	// The damaged PCR must not have been accepted: monotonic time holds.
	var last int64
	for _, p := range s.Packets {
		if p.HasPCR {
			if p.PCRNanos < last {
				t.Fatal("published PCR time regressed")
			}
			last = p.PCRNanos
		}
	}
}

func TestParseFaultSetRejectsUnknown(t *testing.T) {
	_, err := ParseFaultSet("truncated-ts,banana")
	var f *fault.Fault
	if err == nil || !asFault(err, &f) || f.Kind != fault.KindInvalidArg {
		t.Fatalf("unknown fault accepted: %v", err)
	}
	empty, err := ParseFaultSet("")
	if err != nil || len(empty.Names()) != 0 {
		t.Fatal("empty fault set rejected")
	}
}

func TestLegacyManifestDigestsMatchStreams(t *testing.T) {
	specs := DefaultAssets()
	files := TSFiles(specs)
	m := LegacyFor("demo-release", specs, files)
	if m.Version != 1 || len(m.Assets) != len(specs) {
		t.Fatalf("bad legacy fixture: %+v", m)
	}
	raw, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	again, _ := LegacyFor("demo-release", specs, files).Encode()
	if !bytes.Equal(raw, again) {
		t.Fatal("legacy manifest encoding is not deterministic")
	}
	for _, a := range m.Assets {
		sum := sha256.Sum256(files[a.Path])
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			t.Fatalf("asset %s digest does not match its stream", a.Name)
		}
		if a.DurationMS <= 0 || a.Rate <= 0 || a.Channels <= 0 {
			t.Fatalf("incomplete asset %+v", a)
		}
	}
}

func TestFramesForStable(t *testing.T) {
	a := FramesFor(AssetSpec{Name: "intro", Rate: 44100})
	b := FramesFor(AssetSpec{Name: "intro", Rate: 44100})
	if a != b || a <= 0 {
		t.Fatalf("unstable frames %d vs %d", a, b)
	}
	if FramesFor(AssetSpec{Name: "intro", Rate: 44100, Frames: 123}) != 123 {
		t.Fatal("explicit frame count ignored")
	}
}

func findKind(l *fault.List, kind fault.Kind) *fault.Fault {
	for _, f := range l.Items {
		if f.Kind == kind {
			return f
		}
	}
	return nil
}

func asFault(err error, target **fault.Fault) bool {
	if f, ok := err.(*fault.Fault); ok {
		*target = f
		return true
	}
	return false
}
