package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Archive is the canonical ledger whose SHA-256 must survive interruption.
type Archive struct {
	InputDigest string        `json:"input_digest"`
	Items       []ArchiveItem `json:"items"`
}

func (e *engine) archivePath() string { return filepath.Join(e.root, "archive.json") }

func (e *engine) loadArchive() {
	b, err := os.ReadFile(e.archivePath())
	if err != nil {
		return
	}
	var a Archive
	if json.Unmarshal(b, &a) != nil {
		return
	}
	e.items = a.Items
	for i, item := range e.items {
		e.itemIndex[item.Cursor] = i
	}
}

func (e *engine) upsertItem(item ArchiveItem) {
	if idx, ok := e.itemIndex[item.Cursor]; ok {
		e.items[idx] = item
		return
	}
	e.itemIndex[item.Cursor] = len(e.items)
	e.items = append(e.items, item)
}

func (e *engine) writeArchive() error {
	items := append([]ArchiveItem(nil), e.items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Cursor < items[j].Cursor })
	b, err := json.MarshalIndent(Archive{InputDigest: e.inputDigest, Items: items}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return atomicWriteFile(e.archivePath(), b)
}

func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".archive-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
