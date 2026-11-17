package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"rateqa/internal/media"
)

// NewFFmpegExecutor returns the production Executor: fixed-QP mpeg4 encode,
// decode back to Y4M, frame-stream PSNR comparison. Every candidate uses two
// uniquely named temp files removed immediately afterwards, so disk use stays
// bounded by the number of in-flight workers.
func NewFFmpegExecutor(workspace string) Executor {
	var seq uint64
	tmp := filepath.Join(workspace, "tmp")
	return func(ctx context.Context, spec SegmentSpec, qp int) (ExecResult, error) {
		n := atomic.AddUint64(&seq, 1)
		m4v := filepath.Join(tmp, fmt.Sprintf("s%d-q%d-%d.m4v", spec.Ref.Index, qp, n))
		dec := filepath.Join(tmp, fmt.Sprintf("s%d-q%d-%d.y4m", spec.Ref.Index, qp, n))
		defer os.Remove(m4v)
		defer os.Remove(dec)
		size, err := media.Encode(ctx, media.EncodeOptions{
			InputY4M: spec.Ref.Path, OutputM4V: m4v, QP: qp,
		})
		if err != nil {
			return ExecResult{}, err
		}
		if err := media.Decode(ctx, m4v, dec); err != nil {
			return ExecResult{}, err
		}
		stats, err := media.CompareY4M(spec.Ref.Path, dec)
		if err != nil {
			return ExecResult{}, err
		}
		return ExecResult{Bytes: size, Stats: stats}, nil
	}
}
