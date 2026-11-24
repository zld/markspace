// Package crosscheck uses the independent github.com/at-wat/ebml-go decoder as
// an oracle. The offset-aware internal parser is authoritative for fault
// localization (ebml-go cannot report byte offsets), but on intact streams
// both decoders must agree on the number of blocks, their codec payload bytes
// and their per-track timestamps. A disagreement is a pipeline defect, not an
// input fault.
package crosscheck

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/at-wat/ebml-go/mkvcore"

	"mediapack/internal/demux"
)

// ReferenceBlock is one block decoded by the ebml-go reference decoder.
type ReferenceBlock struct {
	Track     uint64
	Timestamp int64 // milliseconds
	Keyframe  bool
	Payload   []byte
}

// DecodeWithEBMLGo reads a complete, intact WebM stream through ebml-go and
// returns every block grouped by track (each group in stream order). The
// reference decoder's multi-track sorter only makes progress when all track
// readers are consumed concurrently, so each reader is drained in its own
// goroutine and results are joined with a WaitGroup.
func DecodeWithEBMLGo(r io.Reader) (map[uint64][]ReferenceBlock, error) {
	readers, err := mkvcore.NewSimpleBlockReader(r)
	if err != nil {
		return nil, fmt.Errorf("ebml-go open: %w", err)
	}
	out := make(map[uint64][]ReferenceBlock)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		errOnce  sync.Once
		firstErr error
	)
	for _, rd := range readers {
		wg.Add(1)
		go func(rd mkvcore.BlockReadCloserWithTrackEntry) {
			defer wg.Done()
			trackNum := rd.TrackEntry().TrackNumber
			var blocks []ReferenceBlock
			for {
				b, keyframe, ts, rerr := rd.Read()
				if rerr == io.EOF {
					break
				}
				if rerr != nil {
					errOnce.Do(func() {
						firstErr = fmt.Errorf("ebml-go read track %d: %w", trackNum, rerr)
					})
					return
				}
				blocks = append(blocks, ReferenceBlock{
					Track:     trackNum,
					Timestamp: ts,
					Keyframe:  keyframe,
					Payload:   append([]byte(nil), b...),
				})
			}
			if cerr := rd.Close(); cerr != nil && firstErr == nil {
				errOnce.Do(func() { firstErr = fmt.Errorf("ebml-go close: %w", cerr) })
			}
			mu.Lock()
			out[trackNum] = blocks
			mu.Unlock()
		}(rd)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// CompareDemuxAgainstReference asserts the internal demuxer and the ebml-go
// oracle produce the same per-track ordered payloads and timestamps.
// Demux nanoseconds are scaled back to milliseconds for comparison.
func CompareDemuxAgainstReference(packets []*demux.Packet,
	ref map[uint64][]ReferenceBlock, scaleNS uint64) error {
	if scaleNS == 0 {
		scaleNS = 1_000_000
	}
	// Group internal packets by input track preserving order.
	internal := make(map[uint64][]*demux.Packet)
	for _, p := range packets {
		internal[uint64(p.InputTrack)] = append(internal[uint64(p.InputTrack)], p)
	}
	if len(internal) != len(ref) {
		return fmt.Errorf("track count mismatch: internal=%d reference=%d",
			len(internal), len(ref))
	}
	for trackNum, refBlocks := range ref {
		got, ok := internal[trackNum]
		if !ok {
			return fmt.Errorf("reference track %d missing from internal demux", trackNum)
		}
		if len(got) != len(refBlocks) {
			return fmt.Errorf("track %d block count mismatch: internal=%d reference=%d",
				trackNum, len(got), len(refBlocks))
		}
		for i, rb := range refBlocks {
			p := got[i]
			if !bytes.Equal(p.Payload, rb.Payload) {
				return fmt.Errorf("track %d block %d payload mismatch", trackNum, i)
			}
			refNS := rb.Timestamp * int64(scaleNS)
			if p.TimestampNS != refNS {
				return fmt.Errorf("track %d block %d timestamp mismatch: internal=%dns reference=%dns",
					trackNum, i, p.TimestampNS, refNS)
			}
		}
	}
	return nil
}
