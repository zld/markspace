// Package demo contains the deterministic medcheck acceptance workflow.
package demo

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zld/medcheck/pkg/bitstream"
	"github.com/zld/medcheck/pkg/evidence"
	"github.com/zld/medcheck/pkg/index"
	"github.com/zld/medcheck/pkg/media"
)

// Options configures the demo/verification run.
type Options struct {
	Seed   int64
	Window int
	Out    string
}

// Run generates all fixtures, exercises every subsystem, writes atomic
// evidence/segment files, and returns the report. Failed expectations are
// recorded in the report rather than returned as Go errors.
func Run(opts Options) (*evidence.Report, error) {
	if opts.Window <= 0 {
		return nil, media.NewError(media.CatInvalidArgument, "", -1, -1, "--window must be positive")
	}
	if opts.Seed < 0 {
		return nil, media.NewError(media.CatInvalidArgument, "", -1, -1, "--seed must be non-negative")
	}
	if opts.Out == "" {
		opts.Out = filepath.Join(os.TempDir(), "media-evidence")
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return nil, media.NewError(media.CatInvalidArgument, "", -1, -1, "create output directory: %v", err)
	}

	rep := evidence.New(opts.Seed, opts.Window)
	codecs := []bitstream.Codec{bitstream.CodecH264, bitstream.CodecH265, bitstream.CodecAV1}
	var primary *analysis
	for _, codec := range codecs {
		f, err := GenerateStream(codec, opts.Seed)
		if err != nil {
			return nil, err
		}
		a, err := analyzeFixture(f)
		if err != nil {
			return nil, err
		}
		if codec == bitstream.CodecH264 {
			primary = a
		}
		digest := media.SHA256Bytes(f.Data).Value
		rep.Codecs[string(codec)] = evidence.CodecEvidence{
			Codec: string(codec), Keyframes: len(a.result.Keyframes()),
			RoundTripRecords: len(a.parsed.Records), IndexHits: indexHits(a.parsed), StreamSHA256: digest,
		}
		rep.Check(string(codec)+"_twelve_keyframes", len(a.result.Keyframes()) == 12, 12, len(a.result.Keyframes()), digest)
		if err := writeFixture(opts.Out, filepath.Join("sources", string(codec)+".bin"), f.Data); err != nil {
			return nil, err
		}
	}
	if primary == nil {
		return nil, media.NewError(media.CatIntegrityMismatch, "", -1, -1, "primary H.264 analysis missing")
	}

	repeat, err := independentRepeat(opts.Seed)
	if err != nil {
		return nil, err
	}
	rep.RoundTripTotal = len(primary.index.Records)
	for i := range primary.index.Records {
		if reflect.DeepEqual(primary.index.Records[i], primary.parsed.Records[i]) {
			rep.RoundTripOK++
		}
	}
	rep.IndexHitTotal = len(primary.parsed.Records)
	rep.IndexHitOK = indexHits(primary.parsed)
	rep.RepeatMatchTotal = len(primary.segments)
	for i, seg := range primary.segments {
		firstHash := primary.hashes[i]
		secondHash := repeat.hashes[i]
		boundaryMatch := i < len(repeat.segments) && repeat.segments[i].Offset == seg.Offset && repeat.segments[i].End == seg.End
		match := firstHash == secondHash && boundaryMatch
		if match {
			rep.RepeatMatchOK++
		}
		name := fmt.Sprintf("segments/segment-%02d.bin", i)
		if err := writeFixture(opts.Out, name, primary.fixture.Data[seg.Offset:seg.End]); err != nil {
			return nil, err
		}
		rep.Segments = append(rep.Segments, evidence.Segment{
			Ordinal: i, TrackID: primary.fixture.TrackID, Codec: string(primary.fixture.Codec),
			Offset: seg.Offset, End: seg.End, Length: seg.Length, SHA256: firstHash,
			RepeatSHA256: secondHash, RepeatMatch: match,
		})
	}
	rep.Check("roundtrip_12_of_12", rep.RoundTripOK == 12, "12/12", fmt.Sprintf("%d/%d", rep.RoundTripOK, rep.RoundTripTotal), "index records JSON round trip")
	rep.Check("index_hit_12_of_12", rep.IndexHitOK == 12, "12/12", fmt.Sprintf("%d/%d", rep.IndexHitOK, rep.IndexHitTotal), "Floor(offset) returns containing keyframe")
	rep.Check("segments_12", len(rep.Segments) == 12, 12, len(rep.Segments), "one segment per keyframe")
	rep.Check("repeat_match_12_of_12", rep.RepeatMatchOK == 12, "12/12", fmt.Sprintf("%d/%d", rep.RepeatMatchOK, rep.RepeatMatchTotal), "same seed yields same boundaries and hashes")

	for name, data := range CorruptFixtures() {
		codec := bitstream.CodecH264
		if filepath.Ext(name) == ".obu" {
			codec = bitstream.CodecAV1
		}
		res, err := bitstream.Scan(bytes.NewReader(data), codec, name)
		if err != nil {
			return nil, err
		}
		if err := writeFixture(opts.Out, name, data); err != nil {
			return nil, err
		}
		rep.Issues = append(rep.Issues, res.Issues...)
		rep.Check("corrupt_record_"+name, len(res.Issues) > 0, ">=1 error", len(res.Issues), "corruption must not be silently skipped")

		corruptIdx, err := index.Build(name, codec, res)
		if err != nil {
			return nil, err
		}
		corruptSegs, err := corruptIdx.Segments(int64(len(data)))
		if err != nil {
			return nil, err
		}
		corruptSegs = index.AnnotateIssues(corruptSegs, res.Issues)
		marked := index.CountCorrupt(corruptSegs)
		if filepath.Ext(name) == ".h264" {
			rep.Check("corrupt_segment_isolated_and_resync_continues", marked == 1 && len(corruptSegs)-marked == 1,
				"1 corrupt + 1 usable", fmt.Sprintf("%d corrupt/%d total", marked, len(corruptSegs)), "later keyframe remains indexed")
		}
	}
	rep.Check("corrupt_nal_recorded", rep.Issues.Has(media.CatTruncatedNAL), "truncated_nal", "present", "track/offset coordinates")
	rep.Check("corrupt_obu_length_recorded", rep.Issues.Has(media.CatIllegalLength), "illegal_length", "present", "LEB128 validation")
	rep.Check("corrupt_obu_truncation_recorded", rep.Issues.Has(media.CatTruncatedOBU), "truncated_obu", "present", "declared OBU size")

	if _, err := runRTPAndTimeline(rep, opts.Window); err != nil {
		return nil, err
	}
	if err := runAssetsAndCache(rep, opts.Out, opts.Seed, primary); err != nil {
		return nil, err
	}
	if _, err := rep.Write(opts.Out); err != nil {
		return nil, err
	}
	return rep, nil
}
