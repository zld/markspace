package engine

import (
	"context"
	"fmt"
	"os"

	"mediatrace/internal/cache"
	"mediatrace/internal/ebml"
	"mediatrace/internal/timeline"
)

func (p Plan) validate() error {
	if p.BudgetPasses < 1 || p.BudgetPasses > 3 {
		return fmt.Errorf("budget-passes must be 1..3, got %d", p.BudgetPasses)
	}
	if p.MaxDecodeSeconds <= 0 {
		return fmt.Errorf("max decode duration must be positive")
	}
	return nil
}

func (r *Runner) scanAndIndex(ctx context.Context, p Plan, store *cache.Store,
	key string, inputSHA []byte, f *os.File, size int64,
) (*ebml.Document, []ebml.IndexEntry, error) {
	builder, err := store.Builder(key, inputSHA)
	if err != nil {
		return nil, nil, err
	}
	var entries []ebml.IndexEntry
	doc, err := ebml.Scan(f, size, p.Limits, func(b ebml.Block) error {
		e := ebml.IndexEntry{
			TrackNo:    b.TrackNo,
			TimeNs:     b.TimeNs,
			ElemOffset: b.ElemOffset,
			DataOffset: b.DataOffset,
			DataSize:   b.DataSize,
		}
		entries = append(entries, e)
		return builder.Append(e)
	})
	if err != nil {
		_ = builder.AbortForTest()
		return nil, nil, err
	}
	if err := store.Commit(key, builder); err != nil {
		return nil, nil, err
	}
	return doc, entries, nil
}

func loadEventsFile(path string) ([]timeline.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return timeline.LoadEvents(f)
}

func alignEntries(entries []ebml.IndexEntry, events []timeline.Event, maxErr int64,
) ([]timeline.Mapping, []timeline.BlockView) {
	views := make([]timeline.BlockView, len(entries))
	for i, e := range entries {
		views[i] = timeline.BlockView{TrackNo: e.TrackNo, TimeNS: e.TimeNs}
	}
	return timeline.Align(views, events, maxErr), views
}

// audioTrackIndex maps a Matroska track number to FFmpeg's zero-based index
// among audio streams.
func audioTrackIndex(doc *ebml.Document, trackNo uint64) int {
	idx := 0
	for _, t := range doc.Tracks {
		if !t.IsAudio {
			continue
		}
		if t.Number == trackNo {
			return idx
		}
		idx++
	}
	return 0
}
