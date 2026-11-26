package publish

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"regress/internal/fault"
	"regress/internal/fixture"
	"regress/internal/plan"
	"regress/internal/scan"
	"regress/internal/worker"
)

// Request is everything the publisher needs to materialize one representation.
type Request struct {
	JobKey     string
	Codec      string
	SourcePath string
	SourceW    int
	SourceH    int
	SourceFPS  int
	Decision   *plan.Decision
}

// ArtifactSet lists the final published files for one representation.
type ArtifactSet struct {
	ManifestPath string
	AuditPath    string
	InitPath     string
	SegmentPaths []string
	Manifest     *Manifest
}

// Publisher owns the staging and publish roots and drives the full write path.
type Publisher struct {
	StagingDir string
	PubDir     string
}

// NewPublisher prepares both directories.
func NewPublisher(stagingDir, pubDir string) (*Publisher, error) {
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(pubDir, 0o755); err != nil {
		return nil, err
	}
	return &Publisher{StagingDir: stagingDir, PubDir: pubDir}, nil
}

// fragmentedEncode renders the selected candidate as a single fragmented MP4
// with one fragment per keyframe, so fragment boundaries match the shared
// keyframe table exactly.
func (p *Publisher) fragmentedEncode(ctx context.Context, req *Request, outMP4 string) error {
	bin, err := fixture.FFMpegPath()
	if err != nil {
		return err
	}
	c := *req.Decision.Selected
	gop := req.Decision.Segments.FramesPerSegment[0]
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", req.SourcePath,
	}
	args = append(args, fixture.TranscodeArgs(req.Codec, c.Width, c.Height, gop, c.BitrateBPS, c.CRF)...)
	args = append(args, "-an",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", outMP4,
	)
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr []byte
	cmd.Stderr = &stderrBuf{buf: &stderr}
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fault.New(fault.StagePublish, fault.CodeSubprocessTimeout, req.Codec,
				"fragmented encode exceeded time budget").Wrap(err)
		}
		return fault.New(fault.StagePublish, fault.CodeSubprocessFailed, req.Codec,
			"fragmented encode failed: "+string(stderr)).Wrap(err)
	}
	return nil
}

// Publish runs the complete write path. No manifest is visible in the publish
// root until every segment has been staged, hashed and promoted successfully.
func (p *Publisher) Publish(ctx context.Context, req *Request) (*ArtifactSet, error) {
	if req.Decision == nil || req.Decision.Selected == nil {
		return nil, fault.New(fault.StagePublish, fault.CodeBudgetUnsatisfiable, req.Codec,
			"refusing to publish without a selected candidate")
	}
	stage := filepath.Join(p.StagingDir, req.JobKey)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, err
	}
	// Clean any stale staging content so a resumed run cannot mix fragments.
	if err := os.RemoveAll(stage); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, err
	}

	fragMP4 := filepath.Join(stage, "fragments.mp4")
	if err := p.fragmentedEncode(ctx, req, fragMP4); err != nil {
		return nil, err
	}
	f, err := os.Open(fragMP4)
	if err != nil {
		return nil, err
	}
	initPath, segPaths, err := SplitFragmentedMP4(f, stage, req.Codec)
	f.Close()
	if err != nil {
		return nil, fault.New(fault.StagePublish, fault.CodeIO, req.Codec, err.Error()).Wrap(err)
	}

	initMeta, err := ParseInit(initPath)
	if err != nil {
		return nil, err
	}
	metas := make([]*SegmentMeta, 0, len(segPaths))
	hashes := make([]string, 0, len(segPaths))
	for _, sp := range segPaths {
		meta, perr := ParseSegment(sp)
		if perr != nil {
			return nil, perr
		}
		meta.Timescale = initMeta.Timescale
		metas = append(metas, meta)
		h, herr := worker.HashFile(sp)
		if herr != nil {
			return nil, herr
		}
		hashes = append(hashes, h)
	}

	initHash, err := worker.HashFile(initPath)
	if err != nil {
		return nil, err
	}
	manifest, err := BuildManifest(req.Codec, req.JobKey, req.Decision.ConfigHash,
		initPath, initHash, metas, hashes)
	if err != nil {
		return nil, err
	}
	trail, err := BuildAuditTrail(metas, fixedSSRC(req.JobKey), rtpPayloadType(req.Codec))
	if err != nil {
		return nil, err
	}
	if err := VerifyAuditTrail(trail, metas); err != nil {
		return nil, err
	}

	// Promote everything atomically; the manifest is written last so an
	// incomplete set can never be referenced.
	pubRoot := filepath.Join(p.PubDir, req.JobKey)
	pubInit := filepath.Join(pubRoot, filepath.Base(initPath))
	if err := worker.AtomicPublish(initPath, pubInit); err != nil {
		return nil, err
	}
	pubSegs := make([]string, 0, len(segPaths))
	for _, sp := range segPaths {
		dest := filepath.Join(pubRoot, filepath.Base(sp))
		if err := worker.AtomicPublish(sp, dest); err != nil {
			return nil, err
		}
		pubSegs = append(pubSegs, dest)
	}
	manifestPath := filepath.Join(pubRoot, "manifest.json")
	if err := WriteManifest(manifest, manifestPath); err != nil {
		return nil, err
	}
	auditPath := filepath.Join(pubRoot, "rtp-audit.json")
	if err := WriteAuditTrail(trail, auditPath); err != nil {
		return nil, err
	}
	return &ArtifactSet{
		ManifestPath: manifestPath,
		AuditPath:    auditPath,
		InitPath:     pubInit,
		SegmentPaths: pubSegs,
		Manifest:     manifest,
	}, nil
}

func rtpPayloadType(codec string) uint8 {
	switch codec {
	case scan.CodecH265:
		return 99
	case scan.CodecAV1:
		return 98
	default:
		return 96
	}
}

// fixedSSRC derives a stable, non-zero SSRC from the job key so repeated runs
// produce identical audit packets.
func fixedSSRC(key string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	if h == 0 {
		h = 1
	}
	return h
}

// stderrBuf adapts a byte slice to io.Writer for command capture.
type stderrBuf struct{ buf *[]byte }

func (s *stderrBuf) Write(p []byte) (int, error) {
	*s.buf = append(*s.buf, p...)
	return len(p), nil
}
