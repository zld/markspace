package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.com/mediavault/internal/fixtures"
	"example.com/mediavault/internal/manifest"
	"example.com/mediavault/internal/store"
)

// DemoSummary is the machine-checkable outcome printed by the demo command.
type DemoSummary struct {
	ManifestDigest  string
	UniquePCM       int
	ManifestEntries int
	ResumeFrom      int
	Committed       int
	RTPWraps        int
	RTPPackets      int
	DriftPPM        int64
	DuplicateRecomp int
	ContainerRecomp int
	ChangedRecomp   int
	ChangedReuse    int
	Rejected        int
	Stages          []string
}

type fixtureSet struct {
	dir         string
	normal      string
	normalCopy  string
	container   string
	changed     string
	interrupted string
	rtp         Input
	rejects     []Input
	rejectNames []string
}

// demoMarker identifies a directory owned by the demo command.
const demoMarker = ".mediavault-demo"

// demoBase is 16384 frames: exactly 32 blocks at the 1 KiB policy.
var demoBase = fixtures.WAVOptions{
	SampleRate: 8000, Channels: 1, Bits: 16, Frames: 16384,
	BlockFrames: 512, ModifyBlock: -1, Seed: 11,
}

func buildFixtures(dir string) (*fixtureSet, error) {
	fdir := filepath.Join(dir, "fixtures")
	if err := os.MkdirAll(fdir, 0o755); err != nil {
		return nil, err
	}
	fs := &fixtureSet{dir: fdir}
	write := func(name string, b []byte) (string, error) {
		p := filepath.Join(fdir, name)
		if err := os.WriteFile(p, b, 0o444); err != nil {
			return "", err
		}
		return p, nil
	}
	var err error
	if fs.normal, err = write("normal.wav", fixtures.WAV(demoBase)); err != nil {
		return nil, err
	}
	if fs.normalCopy, err = write("normal-copy.wav", fixtures.WAV(demoBase)); err != nil {
		return nil, err
	}
	if fs.container, err = write("container-variant.wav", fixtures.ContainerVariant(demoBase)); err != nil {
		return nil, err
	}
	changedOpts := demoBase
	changedOpts.ModifyBlock = 7
	if fs.changed, err = write("changed-block.wav", fixtures.WAV(changedOpts)); err != nil {
		return nil, err
	}
	if fs.interrupted, err = write("interrupted.wav", fixtures.WAV(demoBase)); err != nil {
		return nil, err
	}

	pattern := fixtures.VariablePattern()
	fs.rtp = Input{
		Name: "rtp-drift", Kind: "rtp",
		RTPPackets:    fixtures.RTPPackets(fixtures.RTPOptions{SampleCounts: pattern, Start: 0xFFFFF000, DriftPPM: 250, FrameBytes: 2}),
		RTPFrameBytes: 2,
		RTPSampleRate: 8000,
		RTPClockRate:  8000,
	}

	excessive := fixtures.RTPOptions{SampleCounts: pattern, Start: 1000, DriftPPM: 1500, FrameBytes: 2}
	truncated := fixtures.WAVOptions{SampleRate: 8000, Channels: 1, Bits: 16, Frames: 4096, BlockFrames: 512, ModifyBlock: -1, Truncate: 64}
	unaligned := fixtures.WAVOptions{SampleRate: 8000, Channels: 2, Bits: 16, Frames: 1024, BlockFrames: 512, ModifyBlock: -1, Unaligned: 3}

	truncPath, err := write("truncated.wav", fixtures.WAV(truncated))
	if err != nil {
		return nil, err
	}
	unalignedPath, err := write("unaligned.wav", fixtures.WAV(unaligned))
	if err != nil {
		return nil, err
	}
	corruptMP4, err := write("corrupt.mp4", corruptMP4Bytes())
	if err != nil {
		return nil, err
	}

	fs.rejects = []Input{
		{Name: "truncated", Kind: "wav", Path: truncPath},
		{Name: "unaligned", Kind: "wav", Path: unalignedPath},
		{Name: "rtp-drift-excess", Kind: "rtp", RTPPackets: fixtures.RTPPackets(excessive), RTPFrameBytes: 2, RTPSampleRate: 8000, RTPClockRate: 8000},
		{Name: "corrupt-mp4", Kind: "mp4", Path: corruptMP4},
	}
	fs.rejectNames = []string{"truncated", "unaligned", "rtp-drift-excess", "corrupt-mp4"}
	return fs, nil
}

// corruptMP4Bytes is a structurally valid ftyp followed by an mdat box whose
// declared extent runs far past the end of the file.
func corruptMP4Bytes() []byte {
	return []byte{
		0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', '6', 0, 0, 0, 0, 'i', 's', 'o', '6', 'm', 'p', '4', '1',
		0xFF, 0xFF, 0xFF, 0x00, 'm', 'd', 'a', 't',
	}
}

// RunDemo drives every subsystem and returns the acceptance summary plus the
// sealed manifest. Fixtures that must be rejected are counted, never fatal.
func RunDemo(dir string, out *os.File) (*DemoSummary, *manifest.File, error) {
	if err := prepareDir(dir); err != nil {
		return nil, nil, err
	}
	fs, err := buildFixtures(dir)
	if err != nil {
		return nil, nil, err
	}
	sum := &DemoSummary{}

	r, err := openRunner(dir, out, 0)
	if err != nil {
		return nil, nil, err
	}
	ingest := func(label string, in Input) (Report, error) {
		rep := r.Ingest(in)
		if rep.Rejected {
			return rep, fmt.Errorf("%s: %w", label, rep.Err)
		}
		collectStages(sum, rep)
		return rep, nil
	}

	normal, err := ingest("normal", Input{Name: "normal", Kind: "wav", Path: fs.normal})
	if err != nil {
		return nil, nil, err
	}
	dup, err := ingest("normal-copy", Input{Name: "normal-copy", Kind: "wav", Path: fs.normalCopy})
	if err != nil {
		return nil, nil, err
	}
	sum.DuplicateRecomp = dup.Recomputed
	if dup.Asset.Key != normal.Asset.Key {
		return nil, nil, fmt.Errorf("duplicate bytes produced a different asset key")
	}
	cont, err := ingest("container-variant", Input{Name: "container-variant", Kind: "wav", Path: fs.container})
	if err != nil {
		return nil, nil, err
	}
	sum.ContainerRecomp = cont.Recomputed
	if cont.Asset.Key != normal.Asset.Key {
		return nil, nil, fmt.Errorf("container-only change produced a different asset key")
	}
	chg, err := ingest("changed-block", Input{Name: "changed-block", Kind: "wav", Path: fs.changed})
	if err != nil {
		return nil, nil, err
	}
	sum.ChangedRecomp = chg.Recomputed
	sum.ChangedReuse = chg.Reused

	// Recovery runs against its own clean store so the sink can prove blocks
	// 0 and 1 are committed before the crash.
	resumeDir := filepath.Join(dir, "recovery")
	if err := os.MkdirAll(resumeDir, 0o755); err != nil {
		return nil, nil, err
	}
	crashed, err := openRunner(resumeDir, out, 2)
	if err != nil {
		return nil, nil, err
	}
	first := crashed.Ingest(Input{Name: "interrupted", Kind: "wav", Path: fs.interrupted})
	if first.Rejected || !first.Interrupted {
		return nil, nil, fmt.Errorf("interrupted run did not stop as planned: %v", first.Err)
	}
	sum.Stages = append(sum.Stages, first.Transitions...)
	fmt.Fprintf(out, "interrupted committed_blocks=%d\n", first.Committed)

	resumedRunner, err := openRunner(resumeDir, out, 0)
	if err != nil {
		return nil, nil, err
	}
	resumed := resumedRunner.Ingest(Input{Name: "interrupted", Kind: "wav", Path: fs.interrupted})
	if resumed.Rejected {
		return nil, nil, resumed.Err
	}
	sum.ResumeFrom = resumed.ResumeFrom
	sum.Committed = resumed.Committed
	collectStages(sum, resumed)
	if _, _, err := resumedRunner.Finalize(); err != nil {
		return nil, nil, err
	}

	rtpRep, err := ingest("rtp-drift", fs.rtp)
	if err != nil {
		return nil, nil, err
	}
	sum.RTPWraps = rtpRep.Wraps
	sum.RTPPackets = rtpRep.RTPPackets
	sum.DriftPPM = rtpRep.DriftPPM

	for i, in := range fs.rejects {
		rep := r.Ingest(in)
		if !rep.Rejected {
			return nil, nil, fmt.Errorf("fixture %s was expected to be rejected", fs.rejectNames[i])
		}
		sum.Rejected++
		fmt.Fprintf(out, "expected-rejection %s code=%s\n", in.Name, rep.ErrorCode)
	}

	mf, _, err := r.Finalize()
	if err != nil {
		return nil, nil, err
	}
	sum.ManifestDigest = mf.Digest
	sum.UniquePCM = mf.UniquePCM
	sum.ManifestEntries = len(mf.Entries)
	if err := os.WriteFile(filepath.Join(dir, demoMarker), []byte("mediavault demo\n"), 0o644); err != nil {
		return nil, nil, err
	}
	return sum, mf, nil
}

func collectStages(sum *DemoSummary, rep Report) {
	sum.Stages = append(sum.Stages, rep.Transitions...)
}

// PrintDemo renders the summary lines the acceptance run looks for.
func PrintDemo(out *os.File, sum *DemoSummary) {
	fmt.Fprintln(out, strings.Repeat("-", 60))
	fmt.Fprintf(out, "resume_from_block=%d\n", sum.ResumeFrom)
	fmt.Fprintf(out, "committed_blocks=%d\n", sum.Committed)
	fmt.Fprintf(out, "rtp_wraps=%d\n", sum.RTPWraps)
	fmt.Fprintf(out, "rtp_packets=%d\n", sum.RTPPackets)
	fmt.Fprintf(out, "drift_ppm=%d\n", sum.DriftPPM)
	fmt.Fprintf(out, "duplicate_recomputed_blocks=%d\n", sum.DuplicateRecomp)
	fmt.Fprintf(out, "container_recomputed_pcm_blocks=%d\n", sum.ContainerRecomp)
	fmt.Fprintf(out, "changed_recomputed_blocks=%d\n", sum.ChangedRecomp)
	fmt.Fprintf(out, "changed_reused_blocks=%d\n", sum.ChangedReuse)
	fmt.Fprintf(out, "rejected_fixtures=%d\n", sum.Rejected)
	fmt.Fprintf(out, "manifest_entries=%d\n", sum.ManifestEntries)
	fmt.Fprintf(out, "unique_pcm=%d\n", sum.UniquePCM)
	fmt.Fprintf(out, "manifest_digest=%s\n", sum.ManifestDigest)
	fmt.Fprintf(out, "stages=%s\n", strings.Join(sum.Stages, " | "))
}

// prepareDir makes the demo reproducible. It only clears a directory that is
// empty or that carries the marker a previous demo left behind, so pointing
// --dir at real user data can never destroy it.
func prepareDir(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(dir, 0o755)
		}
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, demoMarker)); err != nil {
		return fmt.Errorf("%s is not empty and was not created by mediavault demo; refusing to clear it", dir)
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func fixtureDigest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// openRunner opens the content store rooted at dir and a runner over it.
func openRunner(dir string, out *os.File, stopAfter int) (*Runner, error) {
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	return New(Options{Store: st, Out: out, StopAfterBlocks: stopAfter})
}
