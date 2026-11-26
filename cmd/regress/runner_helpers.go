package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"

	"cmafrepair/pkg/media/cache"
	"cmafrepair/pkg/media/cmaf"
	"cmafrepair/pkg/media/index"
	"cmafrepair/pkg/media/manifest"
	"cmafrepair/pkg/media/timebase"
)

func itoa(n int) string { return strconv.Itoa(n) }

// handleFragment applies cache lookup, writes the repaired fragment, builds
// the index entry and commits a checkpoint item. GAP fragments are indexed
// as placeholders and never written.
func (r *runner) handleFragment(rep, repOut, src string, f *cmaf.Fragment,
	e cmaf.TimelineEntry, builder *index.Builder) error {

	key := rep + "#" + strconv.FormatUint(uint64(f.Sequence), 10)

	// Parse-cache path: key from file identity + content hash + version.
	id, err := cache.IdentityOf(src)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	sum, err := cache.ContentHashFile(src)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	parseKey := cache.NewParseKey(id, sum)
	if _, out := r.cache.Get(parseKey.Hash() + ":parse"); out.Hit {
		// A hit means the parsed object is unchanged; repair is still run
		// but we avoid re-persisting any derived blob.
	} else {
		_ = r.cache.Put(parseKey.Hash()+":parse", []byte(f.Name))
	}

	if e.Status == cmaf.StatusGap {
		builder.AddGap(e)
		r.rep.gap++
		return nil
	}

	// Resume: skip a fragment already committed and re-verified.
	if r.resume && r.cp.IsVerified(key) {
		r.rep.fragmentsResume++
		if err := builder.AddFragment(f, e); err != nil {
			return &exitError{code: exitUnsafe, msg: err.Error()}
		}
		return nil
	}

	w, err := cmaf.NewWriter(repOut)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	dst, _, _, werr := w.WriteFragment(src, f, e.FixedTfdt, e.Status)
	if werr != nil {
		return &exitError{code: exitUnsafe, msg: werr.Error()}
	}
	// Truncated fragments are written for inspection but are NOT committed
	// to the checkpoint (only complete, checksum-gated items are).
	if e.Status == cmaf.StatusTruncated {
		builder.AddFragment(f, e)
		return nil
	}
	if err := cmaf.VerifyPatch(dst, e.FixedTfdt); err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	rel := filepath.Join(rep, filepath.Base(dst))
	if _, err := r.cp.Commit(key, rel, e.StartMs); err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	if err := builder.AddFragment(f, e); err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	return nil
}

// ensureInit copies the init segment once; it is outside the fragment
// checkpoint but is content-hash guarded by the parse cache.
func (r *runner) ensureInit(rep, repIn, repOut string) error {
	src := filepath.Join(repIn, "init.mp4")
	dst := filepath.Join(repOut, "init.mp4")
	id, err := cache.IdentityOf(src)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	sum, err := cache.ContentHashFile(src)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	key := cache.NewParseKey(id, sum).Hash() + ":init"
	if _, out := r.cache.Get(key); out.Hit && exists(dst) {
		return nil
	}
	w, err := cmaf.NewWriter(repOut)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	if _, _, err := w.CopyInit(src, "init.mp4"); err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	return r.cache.Put(key, []byte(sum))
}

// writeIndex serializes a random-access index under the output tree. The
// index cache key includes the timeline + manifest fingerprints so a
// metadata-only change rebuilds it while parse entries stay cached.
func (r *runner) writeIndex(rep string, ix *index.Index) error {
	timelineHash := cache.Fingerprint(ix.Entries)
	metaHash := cache.Fingerprint(manifest.Metadata{Name: rep})
	// Reuse a representative parse hash as the required parent component.
	indexKey := cache.IndexKey{
		ParseKeyHash:     "rep:" + rep,
		TimelineHash:     timelineHash,
		ManifestMetaHash: metaHash,
		IndexVersion:     cache.IndexVersion,
	}
	payload, err := encodeIndex(ix)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	if err := r.cache.Put(indexKey.Hash()+":index", payload); err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	return writeText(filepath.Join(r.out, rep, "index.json"), string(payload))
}

// verifyAllCommitted re-hashes every checkpoint item and confirms match.
func (r *runner) verifyAllCommitted() bool {
	for _, it := range r.cp.SortedItems() {
		if !r.cp.IsVerified(it.Key) {
			return false
		}
	}
	return true
}

func (r *runner) recordDefects(rep string, defects []*timebase.MediaError) {
	for _, d := range defects {
		r.rep.defects = append(r.rep.defects, defectLine{
			code: string(d.Code), rep: rep, frag: d.Fragment,
			box: d.Box, offset: d.ByteOffset, phase: string(d.Phase),
		})
	}
}

// encodeIndex serializes an index deterministically.
func encodeIndex(ix *index.Index) ([]byte, error) {
	return json.MarshalIndent(ix, "", "  ")
}

// stringBuilderFile renders a media playlist to a string for atomic write.
type stringBuilderFile struct{ buf bytes.Buffer }

func (s *stringBuilderFile) build(pl manifest.MediaPlaylist) string {
	_ = manifest.WriteMediaPlaylist(&s.buf, pl, 1)
	return s.buf.String()
}
