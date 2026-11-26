package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"liveregress/internal/fault"
	"liveregress/internal/manifest"
)

// probeVideoTS demuxes the parallel MPEG-TS rendition of each video segment,
// proves the retained access units match the file, and checks RTP continuity
// where a sidecar exists. The TS rendition is generated from the same access
// units, so a content difference means the demuxer lost or mangled samples.
func (r *Runner) probeVideoTS(lr *LayerReport, pl *manifest.Playlist) {
	for _, seg := range pl.Segments {
		tsRel := trimM4S(seg.Path) + ".ts"
		if _, err := os.Stat(filepath.Join(r.opts.Root, filepath.FromSlash(tsRel))); err != nil {
			continue
		}
		// Copy the TS rendition into fMP4 and prove the payload is unchanged;
		// this exercises the container-only rewrite for transport streams too.
		if sr, rerr := r.remuxTSFile(lr.Layer, tsRel); rerr != nil {
			r.report.AddFault(rerr)
			if fe, ok := fault.As(rerr); ok && !fe.Recoverable {
				lr.Isolated = true
				lr.IsolatedReason = "ts"
				lr.IsolatedOffset = fe.Offset
			}
			continue
		} else {
			lr.TSRemux = append(lr.TSRemux, *sr)
		}
		check, err := r.probeTS(tsRel)
		if err != nil {
			r.report.AddFault(err)
			if fe, ok := fault.As(err); ok && !fe.Recoverable {
				lr.Isolated = true
				lr.IsolatedReason = "ts"
				lr.IsolatedOffset = fe.Offset
			}
			continue
		}
		if lr.TS == nil {
			lr.TS = check
			continue
		}
		lr.TS.Packets += check.Packets
		lr.TS.Samples += check.Samples
		lr.TS.Keyframes += check.Keyframes
		lr.TS.RTPPackets += check.RTPPackets
		lr.TS.RTPSeqGaps += check.RTPSeqGaps
		if check.Truncated {
			lr.TS.Truncated = true
		}
	}
}

func trimM4S(p string) string {
	ext := filepath.Ext(p)
	return p[:len(p)-len(ext)]
}

var _ = fmt.Sprintf
