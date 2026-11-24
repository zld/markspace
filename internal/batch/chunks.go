package batch

import (
	"fmt"

	"mediapack/internal/index"
	"mediapack/internal/maptrack"
	"mediapack/internal/timeline"
)

func commitTimelineChunks(store *index.Store, table *maptrack.Table, tl *timeline.Timeline, chunkSamples int) ([]index.ChunkRecord, int, error) {
	inputByTarget := map[uint32]string{}
	for _, mt := range table.Tracks {
		inputByTarget[mt.TargetID] = mt.InputPath
	}
	if chunkSamples <= 0 {
		chunkSamples = 256
	}
	startCP := store.Checkpoint()
	var records []index.ChunkRecord
	recovered := 0
	sequence := 0

	for _, plan := range tl.Tracks {
		for start := 0; start < len(plan.Samples); start += chunkSamples {
			end := minInt(start+chunkSamples, len(plan.Samples))
			sequence++
			hashes := make([]string, 0, end-start)
			var payloadBytes int64
			var endOffset int64
			for _, s := range plan.Samples[start:end] {
				hashes = append(hashes, s.Hash)
				payloadBytes += int64(len(s.Payload))
				if off := s.Offset + int64(s.Size); off > endOffset {
					endOffset = off
				}
			}
			rec := index.ChunkRecord{
				Sequence:         sequence,
				ChunkID:          fmt.Sprintf("target-%d", plan.TargetID),
				TrackID:          plan.TargetID,
				ChunkIndex:       start / chunkSamples,
				FirstSampleIndex: start,
				SampleCount:      end - start,
				PayloadBytes:     payloadBytes,
				PayloadHashes:    hashes,
				InputPath:        inputByTarget[plan.TargetID],
				InputEndOffset:   endOffset,
			}
			records = append(records, rec)
			if sequence > startCP.LastSequence {
				if store.HadCheckpoint() {
					store.AddRecoveredChunks(1)
				}
				if _, err := store.CommitChunk(rec); err != nil {
					return records, recovered, err
				}
				recovered++
			}
		}
	}
	return records, recovered, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
