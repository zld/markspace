package qcrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/pion/rtp"

	"mediaqc/internal/ebml"
	"mediaqc/internal/remux"
	"mediaqc/internal/stream"
	"mediaqc/internal/timeline"
)

// loadOrScanIndex returns a usable index: loaded from the sidecar when
// the binding validates, otherwise rebuilt by a full scan.
func loadOrScanIndex(inputPath string, in *os.File, size int64, sha [32]byte) (*ebml.Index, string, error) {
	sidecar := ebml.SidecarPath(inputPath)
	if _, err := os.Stat(sidecar); err == nil {
		idx, err := ebml.LoadIndex(sidecar, sha)
		if err == nil {
			return idx, "loaded", nil
		}
		if f := timeline.AsFault(err); f.Class != timeline.ClassIndexMismatch {
			return nil, "", err
		}
		// Digest/version/checksum mismatch: discard and rescan.
		idx, err = rescanIndex(inputPath, in, size, sha)
		if err != nil {
			return nil, "", err
		}
		return idx, "rescanned", nil
	}
	idx, err := rescanIndex(inputPath, in, size, sha)
	return idx, "scanned", err
}

// rescanIndex performs a full scan and persists the fresh index.
func rescanIndex(inputPath string, in *os.File, size int64, sha [32]byte) (*ebml.Index, error) {
	res, err := ebml.Scan(in, size)
	if err != nil {
		return nil, err
	}
	idx := ebml.Build(res, sha)
	if err := ebml.SaveIndex(ebml.SidecarPath(inputPath), idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// repairPackets runs every indexed block through the repairer and
// produces the stream packet list plus per-packet durations.
func repairPackets(idx *ebml.Index, maxSkewNs int64) ([]stream.Packet, []uint32, *timeline.RepairStats, error) {
	kinds := map[uint64]timeline.TrackKind{}
	for _, t := range idx.Tracks {
		kinds[t.Number] = t.Kind()
	}
	clk := timeline.NewClock(idx.TimestampScale)
	cfg := timeline.DefaultRepairConfig()
	cfg.MaxSkewNs = maxSkewNs
	rep := timeline.NewRepairer(clk, cfg)

	packets := make([]stream.Packet, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		ns, err := rep.Process(e.Track, kinds[e.Track], e.RawTimecode, e.Offset)
		if err != nil {
			return nil, nil, &rep.Stats, err
		}
		packets = append(packets, stream.Packet{
			Track:    e.Track,
			Ns:       ns,
			Keyframe: e.Keyframe,
			Offset:   e.Offset,
			ReadOff:  e.PayloadOff,
			ReadLen:  e.PayloadLen,
		})
	}
	// Per-track durations: delta to the next packet of the same track.
	lastNs := map[uint64]int64{}
	lastDur := map[uint64]int64{}
	durs := make([]uint32, len(packets))
	for i := len(packets) - 1; i >= 0; i-- {
		p := packets[i]
		d := int64(1_000_000)
		if next, ok := lastNs[p.Track]; ok && next > p.Ns {
			d = next - p.Ns
		} else if prev, ok := lastDur[p.Track]; ok {
			d = prev
		}
		lastNs[p.Track] = p.Ns
		lastDur[p.Track] = d
		durs[i] = uint32(d)
	}
	return packets, durs, &rep.Stats, nil
}

// streamAndRemux runs the bounded-queue execution stage into the remux
// writer on a temp file. The final rename happens in commitOutput.
func streamAndRemux(ctx context.Context, opts Options, idx *ebml.Index, packets []stream.Packet, durs []uint32, mapping map[uint64]uint64, sum *remux.Summary) error {
	if err := os.MkdirAll(filepath.Dir(opts.Output), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	tmp := opts.Output + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w, err := remux.NewWriter(out)
	if err != nil {
		out.Close()
		return err
	}
	trackBySource := map[uint64]*remux.Track{}
	codecBySource := map[uint64]string{}
	for _, t := range idx.Tracks {
		codecBySource[t.Number] = t.Codec
	}
	// Deterministic track order: sort mapped sources before AddTrack.
	sources := make([]uint64, 0, len(mapping))
	for src := range mapping {
		sources = append(sources, src)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })
	for _, src := range sources {
		tr, err := w.AddTrack(src, mapping[src], codecBySource[src])
		if err != nil {
			out.Close()
			return err
		}
		trackBySource[src] = tr
	}

	in, err := os.Open(opts.Input)
	if err != nil {
		out.Close()
		return err
	}
	defer in.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	consumed := 0
	i := 0
	stats, err := stream.Run(ctx, in, packets, stream.RunnerConfig{
		QueueCap:      opts.QueueCap,
		ConsumerDelay: opts.ConsumerDelay,
	}, func(p stream.Packet) error {
		tr := trackBySource[p.Track]
		if tr == nil {
			return timeline.NewFault(timeline.ClassTrackMapping, p.Offset, p.Track,
				"no output track for source %d", p.Track)
		}
		if err := w.WriteSample(tr, p.Data, p.Keyframe, durs[i]); err != nil {
			return err
		}
		i++
		consumed++
		if opts.CancelAfter > 0 && consumed >= opts.CancelAfter {
			cancel()
		}
		return nil
	})
	if err != nil {
		out.Close()
		return err
	}
	if err := w.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	sum.Packets = stats.Packets
	sum.PeakBufferBytes = stats.PeakBufferBytes
	sum.P95LatencyMs = timeline.NsToMs(int64(remux.P95(stats.Latencies)))
	return nil
}

// simulateRTPRecovery models the network return path: payloads are
// packetized into RTP, subjected to deterministic reorder/duplication/
// loss, and reordered by the recovery window. Only transport sequencing
// is touched; the media clock is unaffected.
func simulateRTPRecovery(packets []stream.Packet) float64 {
	n := len(packets)
	if n > 200 {
		n = 200
	}
	if n == 0 {
		return 1
	}
	streamPkts := make([]*rtp.Packet, n)
	for i := 0; i < n; i++ {
		streamPkts[i] = &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: uint16(1000 + i),
				Timestamp:      uint32(packets[i].Ns / 1_000_000),
				SSRC:           0x6D656469, // "medi"
			},
			Payload: []byte{byte(i), byte(i >> 8)},
		}
	}
	_, st := remux.ReorderRTP(remux.SimulateLoss(streamPkts), 8)
	return st.Rate
}

// commitOutput atomically renames the temp output into place and
// returns the SHA-256 of the committed bytes.
func commitOutput(output string) (string, error) {
	tmp := output + ".tmp"
	if err := os.Rename(tmp, output); err != nil {
		return "", err
	}
	f, err := os.Open(output)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
