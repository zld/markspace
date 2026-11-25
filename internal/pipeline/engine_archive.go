package pipeline

import (
	"mediavault/internal/archive"
	"mediavault/internal/report"
)

// archiveRows adds rows to the index and flushes them in chunks, honoring
// the InterruptAfter policy. Rows already committed (re-delivery or a
// resumed run) are skipped by the index's stable-key dedup. When the
// configured number of chunks has been written it returns ErrInterrupted
// so the adapter can exit; resume continues from the last complete chunk.
func (e *Engine) archiveRows(idx *archive.Index, rows []archive.ManifestRow, b *report.Builder) ([]archive.Chunk, error) {
	added := 0
	for _, r := range rows {
		if idx.AddRow(r) {
			added++
		}
	}
	b.AddRows(idx.Rows())

	var committed []archive.Chunk
	startChunks := len(idx.Chunks())
	for {
		// Stop once every buffered row is covered by a committed chunk.
		committedRows := 0
		if n := len(idx.Chunks()); n > 0 {
			committedRows = idx.Chunks()[n-1].RowEnd
		}
		if committedRows >= idx.Len() {
			break
		}
		ch, err := idx.FlushChunk()
		if err != nil {
			return committed, err
		}
		if ch == nil {
			break
		}
		committed = append(committed, *ch)
		flushed := len(idx.Chunks()) - startChunks
		if e.cfg.InterruptAfter > 0 && flushed >= e.cfg.InterruptAfter {
			b.SetChunks(idx.Chunks())
			return committed, ErrInterrupted
		}
	}
	return idx.Chunks(), nil
}
