package demo

import (
	"bytes"
	"context"
	"fmt"

	"mediapipeline/internal/evidence"
	"mediapipeline/internal/pipe"
	"mediapipeline/pkg/media"
)

func streamTrack(track media.TrackInfo, cfg Config) (pipe.Stats, error) {
	type group struct {
		data    []byte
		segment int
	}
	var groups []group
	var cur bytes.Buffer
	segment := 0
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		groups = append(groups, group{data: append([]byte(nil), cur.Bytes()...), segment: segment})
		segment++
		cur.Reset()
	}
	for _, s := range track.Samples {
		if cur.Len() > 0 && cur.Len()+len(s.Data) > cfg.ChunkBytes {
			flush()
		}
		cur.Write(s.Data)
	}
	flush()

	return pipe.Run[media.Sample](
		context.Background(),
		pipe.Config{Workers: cfg.Workers, Queue: cfg.Queue},
		len(groups),
		func(_ context.Context, i int) (pipe.Chunk[media.Sample], error) {
			return pipe.Chunk[media.Sample]{Index: i, TrackID: track.TrackID, Data: groups[i].data}, nil
		},
		func(_ context.Context, c pipe.Chunk[media.Sample]) error {
			got := evidence.SHA256Bytes(c.Data)
			if c.Index >= len(track.Segments) {
				return fmt.Errorf("pipe: chunk %d has no segment evidence", c.Index)
			}
			if want := track.Segments[c.Index].SHA256; want != "" && got != want {
				return fmt.Errorf("pipe: segment %d hash mismatch", c.Index)
			}
			return nil
		},
	)
}
