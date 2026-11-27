package txn

import (
	"os"

	"media.rebuild/internal/journal"
	"media.rebuild/internal/model"
)

// Rollback unpublishes a task: it removes the published artifact directory and
// journals an abort marker. A subsequent run starts from a clean slate and
// republishes exactly once. The manifest is removed only when it describes
// this task, so rolling back one task cannot strand another task's manifest.
func (e *Engine) Rollback(task string) (int, error) {
	if task == "" {
		return 0, model.New(model.CodeInvalidArgument, model.Source{Name: e.root, Offset: -1}, "rollback needs a task", nil)
	}
	published := e.publishedDir(task)
	removed := 0
	if dirExists(published) {
		entries, err := os.ReadDir(published)
		if err != nil {
			return 0, model.New(model.CodeIO, model.Source{Name: published, Offset: -1}, "list published artifacts", err)
		}
		removed = len(entries)
		if err := os.RemoveAll(published); err != nil {
			return 0, model.New(model.CodeIO, model.Source{Name: published, Offset: -1}, "remove published artifacts", err)
		}
	}
	if dirExists(e.stagedDir(task)) {
		if err := os.RemoveAll(e.stagedDir(task)); err != nil {
			return removed, model.New(model.CodeIO, model.Source{Name: e.stagedDir(task), Offset: -1}, "remove staging", err)
		}
	}
	if m, err := ReadManifest(e.root); err == nil && m.Task == task {
		if err := os.Remove(ManifestPath(e.root)); err != nil && !os.IsNotExist(err) {
			return removed, model.New(model.CodeIO, model.Source{Name: ManifestPath(e.root), Offset: -1}, "remove manifest", err)
		}
	}
	if _, err := e.log.Append(journal.Record{Type: journal.TypeAbort, Task: task}); err != nil {
		return removed, err
	}
	return removed, nil
}
