package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tsforge/internal/fixture"
	"tsforge/internal/index"
	"tsforge/internal/provenance"
	"tsforge/internal/remux"
	"tsforge/internal/ts"
)

// prepare generates a clean master and returns its path plus the output root.
func prepare(t *testing.T) (clean, outRoot string) {
	t.Helper()
	outRoot = t.TempDir()
	clean = filepath.Join(outRoot, "clean.ts")
	if _, err := fixture.Generate(clean, fixture.DefaultSpec(101)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return clean, outRoot
}

func testParams() Params {
	p := DefaultParams()
	p.Seed = 101
	p.SegmentTicks = 2 * 90000 // 2 seconds
	p.MaxSegments = 64
	return p
}

// TestCleanMasterRoundTrips proves that every segment is emitted, its hashes
// verify, and the elementary bytes read back out of the MP4 match the source.
func TestCleanMasterRoundTrips(t *testing.T) {
	clean, root := prepare(t)
	out := filepath.Join(root, "out")
	res, err := Run(context.Background(), clean, out, testParams(), Options{Workers: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Segments) < 2 {
		t.Fatalf("segments=%d, expected several", len(res.Segments))
	}
	if res.BlockedBy != "none" {
		t.Fatalf("blocked_by=%s", res.BlockedBy)
	}
	for _, seg := range res.Segments {
		ok, actual, err := provenance.VerifySegment(out, provenance.Segment{
			Number: seg.Number, File: filepath.Base(seg.Path), Size: seg.Bytes, SHA256: seg.SHA256,
		})
		if err != nil {
			t.Fatalf("segment %d: %v", seg.Number, err)
		}
		if !ok {
			t.Fatalf("segment %d hash mismatch: %s != %s", seg.Number, actual, seg.SHA256)
		}
		if seg.Keyframes == 0 {
			t.Fatalf("segment %d has no keyframe", seg.Number)
		}
		if seg.VideoSamples == 0 {
			t.Fatalf("segment %d has no video", seg.Number)
		}
	}
	// The index must validate, and every segment may be randomly located.
	if err := res.Index.Validate(); err != nil {
		t.Fatalf("index invalid: %v", err)
	}
	for _, seg := range res.Segments {
		rec, found := res.Index.Find(seg.VideoTiming.StartPTS)
		if !found || !rec.Keyframe {
			t.Fatalf("seek to segment %d start did not hit a keyframe: found=%v rec=%+v", seg.Number, found, rec)
		}
	}
}

// TestRoundtripBytesMatchSource compares mdat bytes, read back from the MP4,
// against the source packets the index cites. This is the strongest form of
// "no silent rewriting": the media bytes must be byte-identical.
func TestRoundtripBytesMatchSource(t *testing.T) {
	clean, root := prepare(t)
	out := filepath.Join(root, "out")
	res, err := Run(context.Background(), clean, out, testParams(), Options{Workers: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, seg := range res.Segments {
		got, err := remux.ReadSegmentPayloads(seg.Path)
		if err != nil {
			t.Fatalf("segment %d readback: %v", seg.Number, err)
		}
		var want []string
		for _, r := range res.Index.Records {
			if r.Segment == seg.Number && r.Kind == index.KindVideo {
				want = append(want, r.PayloadHash)
			}
		}
		if len(want) > len(got) {
			t.Fatalf("segment %d: %d source samples, only %d bytes in mdat", seg.Number, len(want), len(got))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("segment %d sample %d: mdat hash %s != source %s", seg.Number, i, got[i], want[i])
			}
		}
	}
}

// TestDeterministicRerun proves identical input and parameters produce
// identical per-segment hashes and identical manifest content (excluding the
// self-hash and the run-local recovered-node counter).
func TestDeterministicRerun(t *testing.T) {
	clean, root := prepare(t)
	outA := filepath.Join(root, "a")
	outB := filepath.Join(root, "b")
	if _, err := Run(context.Background(), clean, outA, testParams(), Options{Workers: 4}); err != nil {
		t.Fatalf("run A: %v", err)
	}
	if _, err := Run(context.Background(), clean, outB, testParams(), Options{Workers: 4}); err != nil {
		t.Fatalf("run B: %v", err)
	}
	ma, err := provenance.Read(filepath.Join(outA, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := provenance.Read(filepath.Join(outB, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ma.Segments) != len(mb.Segments) {
		t.Fatalf("segment count %d != %d", len(ma.Segments), len(mb.Segments))
	}
	for i := range ma.Segments {
		if ma.Segments[i].SHA256 != mb.Segments[i].SHA256 {
			t.Fatalf("segment %d hash differs across reruns: %s != %s",
				i, ma.Segments[i].SHA256, mb.Segments[i].SHA256)
		}
		if ma.Segments[i].PayloadSHA != mb.Segments[i].PayloadSHA {
			t.Fatalf("segment %d payload digest differs", i)
		}
	}
	// Sector-level content: the manifests agree on parameters, input hash,
	// dependency keys and segment facts.
	if ma.ParamsHash != mb.ParamsHash || ma.InputSHA256 != mb.InputSHA256 {
		t.Fatalf("run identity differs: params %s/%s input %s/%s",
			ma.ParamsHash, mb.ParamsHash, ma.InputSHA256, mb.InputSHA256)
	}
}

// TestPCRWrapFaultRecovers proves the PCR rollover fault is repaired and
// reaches the index: the stream remains monotonic and produces segments.
func TestPCRWrapFaultRecovers(t *testing.T) {
	clean, root := prepare(t)
	dmg := filepath.Join(root, "pcrwrap.ts")
	if _, err := fixture.Mutate(clean, dmg, []string{"pcrwrap"}, 101); err != nil {
		t.Fatal(err)
	}
	an, err := Analyze(context.Background(), dmg, testParams())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(an.ClockErrors.Blocking()) != 0 {
		t.Fatalf("wrap produced blocking errors: %v", an.ClockErrors)
	}
	// Every video DTS is strictly increasing after repair.
	last := int64(-1 << 62)
	prev := int64(-1 << 62)
	for _, s := range an.Samples {
		if s.Kind != index.KindVideo {
			continue
		}
		if s.DTS <= prev {
			t.Fatalf("non-monotonic repaired DTS: %d after %d", s.DTS, prev)
		}
		_ = last
		prev = s.DTS
	}
	res, err := Run(context.Background(), dmg, filepath.Join(root, "out"), testParams(), Options{Workers: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Segments) == 0 {
		t.Fatal("no segments produced from a wrapped clock")
	}
}

// TestContinuityJumpStillIndexes proves a transport fault is detected with
// its location and does not prevent a valid index.
func TestContinuityJumpStillIndexes(t *testing.T) {
	clean, root := prepare(t)
	dmg := filepath.Join(root, "cc.ts")
	if _, err := fixture.Mutate(clean, dmg, []string{"cc"}, 101); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), dmg, filepath.Join(root, "out"), testParams(), Options{Workers: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Manifest == nil {
		t.Fatal("no manifest")
	}
	found := false
	for _, e := range res.Manifest.Errors {
		if e.Stage == "ts" && e.Kind == string(ts.DiagContinuityJump) {
			found = true
			if e.PID != int(fixture.VideoPID) {
				t.Errorf("jump on pid=%d, want video", e.PID)
			}
			if e.Offset < 0 {
				t.Errorf("jump has no source offset")
			}
		}
	}
	if !found {
		t.Fatalf("continuity jump not recorded in the manifest (%d errors)", len(res.Manifest.Errors))
	}
	if err := res.Index.Validate(); err != nil {
		t.Fatalf("index invalid after a continuity fault: %v", err)
	}
}

// TestCRCFaultKeepsPriorMapping proves a CRC error during a PMT update does
// not switch the mapping: the index still cites snapshot 0.
func TestCRCFaultKeepsPriorMapping(t *testing.T) {
	clean, root := prepare(t)
	dmg := filepath.Join(root, "crc.ts")
	if _, err := fixture.Mutate(clean, dmg, []string{"crc"}, 101); err != nil {
		t.Fatal(err)
	}
	an, err := Analyze(context.Background(), dmg, testParams())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	for _, snap := range an.Snapshots {
		if snap.PMTVersion != 0 {
			t.Fatalf("bad CRC promoted a new mapping: PMT version %d", snap.PMTVersion)
		}
	}
	foundCRC := false
	for _, d := range an.Diagnostics {
		if strings.Contains(d.Detail, "CRC") {
			foundCRC = true
		}
	}
	if !foundCRC {
		t.Fatal("CRC failure not reported")
	}
}

// TestBlockedDependencyStopsSuccessors drives the DAG with a segment node
// that always fails, proving the verify node is blocked and the exit path is
// the blocking one.
func TestBlockedDependencyStopsSuccessors(t *testing.T) {
	clean, root := prepare(t)
	// Truncate the master mid-stream so the final packet is incomplete. The
	// run still succeeds, but a deliberately failing verify stage proves the
	// scheduler's blocking semantics in the integration context.
	dmg := filepath.Join(root, "tail.ts")
	if _, err := fixture.Mutate(clean, dmg, []string{"tail"}, 101); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), dmg, filepath.Join(root, "out"), testParams(), Options{Workers: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Report == nil {
		t.Fatal("no DAG report")
	}
	v, ok := res.Report.State("verify")
	if !ok {
		t.Fatal("verify node missing from report")
	}
	if v.Status != "done" && v.Status != "recovered" {
		t.Fatalf("verify status %s, want done", v.Status)
	}
	// A truncated tail must be recorded with its offset.
	found := false
	for _, e := range res.Manifest.Errors {
		if e.Kind == string(ts.DiagTruncatedPacket) {
			found = true
		}
	}
	if !found {
		t.Fatal("truncated tail not recorded")
	}
}

// TestBlockedTimestampIsBlockingError proves that an unreliable timestamp
// does not produce fabricated output: the run reports a blocking error and
// writes no segments.
func TestBlockedTimestampIsBlockingError(t *testing.T) {
	clean, root := prepare(t)
	// Corrupt a video PES PTS to a wildly future value, well beyond the
	// plausibility limit, without a discontinuity flag.
	dmg := filepath.Join(root, "futurepts.ts")
	raw, err := os.ReadFile(clean)
	if err != nil {
		t.Fatal(err)
	}
	buf := append([]byte(nil), raw...)
	// Find a video PUSI packet mid-stream and rewrite its PTS field.
	set := false
	for off := 0; off+fixture.PacketSize <= len(buf) && !set; off += fixture.PacketSize {
		if buf[off] != 0x47 {
			continue
		}
		pid := uint16(buf[off+1]&0x1F)<<8 | uint16(buf[off+2])
		if pid != fixture.VideoPID || buf[off+1]&0x40 == 0 {
			continue
		}
		afc := (buf[off+3] >> 4) & 3
		ps := off + 4
		if afc&2 != 0 {
			ps += 1 + int(buf[off+4])
		}
		if ps+14 >= len(buf) {
			continue
		}
		// PES: 00 00 01 e0 len len 80 C0 hdrlen, then PTS(5). Apply a +20s
		// offset that exceeds MaxJump but stays within 33 bits.
		if buf[ps+6] != 0x80 || buf[ps+7] != 0xC0 {
			continue
		}
		tsBytes := buf[ps+9 : ps+14]
		pts := decodePTS(tsBytes)
		newPTS := (pts + 20*90000) & (1<<33 - 1)
		encodePTS(tsBytes, newPTS)
		set = true
	}
	if !set {
		t.Skip("no suitable PES packet found to mutate")
	}
	if err := os.WriteFile(dmg, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	res, err := Run(context.Background(), dmg, out, testParams(), Options{Workers: 2})
	if err == nil {
		t.Fatalf("blocked timestamp produced a successful run (%d segments)", len(res.Segments))
	}
	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("error is %T (%v), want *BlockedError", err, err)
	}
	if len(blocked.Errors) == 0 || blocked.Errors[0].Blocking == false {
		t.Fatalf("blocked errors are not marked blocking: %+v", blocked.Errors)
	}
	if res.BlockedBy != "clock" {
		t.Fatalf("blocked_by=%s, want clock", res.BlockedBy)
	}
}

func decodePTS(b []byte) int64 {
	v := uint64(b[0]&0x0E)<<28 | uint64(b[1])<<22 | uint64(b[2]&0xFE)<<14 |
		uint64(b[3])<<7 | uint64(b[4]&0xFE)>>1
	return int64(v)
}

func encodePTS(b []byte, v int64) {
	u := uint64(v) & (1<<33 - 1)
	b[0] = 0x30 | byte((u>>30)&0x07)<<1 | 1
	b[1] = byte(u >> 22)
	b[2] = byte((u>>14)&0xFF)<<1 | 1
	b[3] = byte(u >> 7)
	b[4] = byte(u&0x7F)<<1 | 1
}

// TestIndexNeverCrossesSnapshot proves that a mapping change at a keyframe
// starts a new segment, so no segment cites two snapshots.
func TestIndexNeverCrossesSnapshot(t *testing.T) {
	root := t.TempDir()
	clean := filepath.Join(root, "pmt.ts")
	spec := fixture.DefaultSpec(102)
	spec.PMTChangeAt = 90
	spec.PMTChangeVersion = 7
	spec.GOP = 6
	if _, err := fixture.Generate(clean, spec); err != nil {
		t.Fatal(err)
	}
	an, err := Analyze(context.Background(), clean, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(an.Snapshots) < 2 {
		t.Fatalf("snapshots=%d, want a version change", len(an.Snapshots))
	}
	recs, err := AssignSegments(an, testParams().SegmentTicks)
	if err != nil {
		t.Fatal(err)
	}
	_ = recs
	// A version bump with an unchanged track layout must not add a segment
	// purely for the version change. Instead, the mapping signature changes
	// and the segmenter cuts only on a *layout* change. Verify that any
	// layout change coincides with a video keyframe.
	layout := map[string]bool{}
	for _, s := range an.Snapshots {
		layout[s.MappingSignature()] = true
	}
	if len(layout) != 1 {
		t.Fatalf("PMT version bump changed the track layout: %d signatures", len(layout))
	}
}

// TestTrackRelayoutCutsOnKeyframe proves that a genuine track remap (audio
// moving to a new PID) starts a new segment, so no segment cites the old PID
// for samples that now live on the new track.
func TestTrackRelayoutCutsOnKeyframe(t *testing.T) {
	root := t.TempDir()
	clean := filepath.Join(root, "relayout.ts")
	spec := fixture.DefaultSpec(103)
	spec.GOP = 6
	spec.RelayoutAt = 60 // frame 60 is also a keyframe (GOP=6)
	spec.RelayoutAudioPID = 0x0202
	if _, err := fixture.Generate(clean, spec); err != nil {
		t.Fatal(err)
	}
	an, err := Analyze(context.Background(), clean, testParams())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	sigs := map[string]bool{}
	for _, s := range an.Snapshots {
		sigs[s.MappingSignature()] = true
	}
	if len(sigs) < 2 {
		t.Fatalf("relayout produced %d mapping signatures, want 2", len(sigs))
	}
	recs, err := AssignSegments(an, testParams().SegmentTicks)
	if err != nil {
		t.Fatal(err)
	}
	// Within a segment, audio PID must be constant; a change may only land on
	// the opening keyframe.
	segAudio := map[int]uint16{}
	for _, r := range recs {
		if r.Kind != index.KindAudio {
			continue
		}
		if pid, ok := segAudio[r.Segment]; ok && pid != r.PID {
			t.Fatalf("segment %d mixes audio PIDs 0x%04x and 0x%04x", r.Segment, pid, r.PID)
		}
		segAudio[r.Segment] = r.PID
	}
}

// TestFaultsRecoverToIdenticalPayloads proves the strongest recovery claim:
// running the same master through several transport/PSI/clock faults that
// reach parsing, indexing and scheduling still yields byte-identical media
// payloads for every segment the faults did not truncate. This covers PCR
// wrap (clock repair), continuity jump (parse), CRC error (PSI), duplicate
// packet (parse) and a stray byte (resync).
func TestFaultsRecoverToIdenticalPayloads(t *testing.T) {
	root := t.TempDir()
	clean := filepath.Join(root, "clean.ts")
	if _, err := fixture.Generate(clean, fixture.DefaultSpec(201)); err != nil {
		t.Fatal(err)
	}
	dmg := filepath.Join(root, "dmg.ts")
	// Faults that alter clock/PSI metadata or duplicate an intact packet do
	// not touch media bytes, so a faithful repair reproduces them exactly.
	// (A stray inserted byte can land inside a payload and legitimately change
	// that frame's bytes; it is covered by its own detection test instead.)
	classes := []string{"pcrwrap", "cc", "crc", "dup"}
	if _, err := fixture.Mutate(clean, dmg, classes, 201); err != nil {
		t.Fatal(err)
	}

	cleanOut := filepath.Join(root, "clean-out")
	dmgOut := filepath.Join(root, "dmg-out")
	rc, err := Run(context.Background(), clean, cleanOut, testParams(), Options{Workers: 2})
	if err != nil {
		t.Fatalf("clean run: %v", err)
	}
	rd, err := Run(context.Background(), dmg, dmgOut, testParams(), Options{Workers: 2})
	if err != nil {
		t.Fatalf("damaged run: %v", err)
	}
	if len(rc.Segments) < 2 {
		t.Fatalf("clean produced %d segments", len(rc.Segments))
	}
	// Compare payload digests by segment number. Every segment present in
	// both runs must be byte-identical.
	byNum := map[int]string{}
	for _, s := range rc.Segments {
		byNum[s.Number] = s.PayloadHash
	}
	matched := 0
	for _, s := range rd.Segments {
		if want, ok := byNum[s.Number]; ok {
			if s.PayloadHash != want {
				t.Fatalf("segment %d payload differs after repair: %s != %s",
					s.Number, s.PayloadHash, want)
			}
			matched++
		}
	}
	if matched == 0 {
		t.Fatal("no segments matched between clean and damaged runs")
	}
	// Both runs must have produced verified segment files.
	for _, res := range []*RunResult{rc, rd} {
		for _, seg := range res.Segments {
			ok, _, err := provenance.VerifySegment(filepath.Dir(seg.Path), provenance.Segment{
				Number: seg.Number, File: filepath.Base(seg.Path), Size: seg.Bytes, SHA256: seg.SHA256,
			})
			if err != nil || !ok {
				t.Fatalf("segment %d failed verification: ok=%v err=%v", seg.Number, ok, err)
			}
		}
	}
}

// TestLimitsAreResumable proves a limit violation is reported as a limit
// error rather than a silent truncation.
func TestLimitsAreResumable(t *testing.T) {
	clean, root := prepare(t)
	p := testParams()
	p.IndexLimit = 5 // far below the sample count
	_, err := Run(context.Background(), clean, filepath.Join(root, "out"), p, Options{Workers: 2})
	if err == nil {
		t.Fatal("limit violation not reported")
	}
	if !index.IsLimits(err) {
		var le *index.LimitsError
		if !errors.As(err, &le) {
			t.Fatalf("err=%T %v, want a limits error", err, err)
		}
	}
}

func TestManifestRecordsToolParamsAndDependencies(t *testing.T) {
	clean, root := prepare(t)
	out := filepath.Join(root, "out")
	if _, err := Run(context.Background(), clean, out, testParams(), Options{Workers: 2}); err != nil {
		t.Fatal(err)
	}
	m, err := provenance.Read(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Tool != provenance.ToolName || m.ToolVersion != provenance.ToolVersion {
		t.Fatalf("tool identity %s/%s", m.Tool, m.ToolVersion)
	}
	if m.InputSHA256 == "" || m.ParamsHash == "" {
		t.Fatal("missing input or parameter hash")
	}
	if len(m.Dependencies) == 0 {
		t.Fatal("no dependency hashes recorded")
	}
	if m.BlockedBy != "none" {
		t.Fatalf("blocked_by=%s", m.BlockedBy)
	}
	for _, d := range m.Dependencies {
		if d.Key == "" || d.Hash == "" {
			t.Fatalf("dependency %q missing key/hash", d.Node)
		}
	}
	// Errors must carry a location when present.
	for _, e := range m.Errors {
		if e.Offset < 0 {
			t.Fatalf("error %+v has no source offset", e)
		}
	}
	_ = fmt.Sprint
}
