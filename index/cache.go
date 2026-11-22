package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mediacore/media"
)

// CacheState is the outcome of a cache load attempt.
type CacheState string

const (
	CacheHit     CacheState = "hit"
	CacheMiss    CacheState = "miss"
	CacheStale   CacheState = "stale"
	CacheVersion CacheState = "version_mismatch"
	CacheCorrupt CacheState = "corrupt"
	CacheResidue CacheState = "temp_residue"
)

// CacheReport describes what the cache did and why, for terminal output.
type CacheReport struct {
	State   CacheState   `json:"state"`
	Rebuilt bool         `json:"rebuilt"`
	Reason  string       `json:"reason,omitempty"`
	Error   *media.Error `json:"error,omitempty"`
	Path    string       `json:"path"`
	Temp    string       `json:"temp,omitempty"`
}

// TempPath returns the sibling temporary path used for an atomic replace.
func TempPath(path string) string {
	return path + ".tmp"
}

// cacheEnvelope is the on-disk cache document. The nested index remains a
// standalone, independently verifiable document, while the envelope binds it
// to the exact input bytes and the cache writer's version.
type cacheEnvelope struct {
	CacheVersion int    `json:"cache_version"`
	IndexVersion int    `json:"index_version"`
	InputDigest  string `json:"input_digest"`
	IndexDigest  string `json:"index_digest"`
	EnvelopeHash string `json:"envelope_hash"`
	Index        *Index `json:"index"`
}

func (env *cacheEnvelope) hash() string {
	h := sha256.New()
	h.Write([]byte(env.InputDigest))
	h.Write([]byte{0})
	h.Write([]byte(env.IndexDigest))
	h.Write([]byte{0})
	var v [2]byte
	v[0] = byte(env.IndexVersion)
	v[1] = byte(env.CacheVersion)
	h.Write(v[:])
	return hex.EncodeToString(h.Sum(nil))
}

// WriteCache atomically writes idx bound to inputDigest. The document is
// written to a sibling temp file, fsynced, then renamed over the target so a
// crash can never expose a partially written cache at the canonical path.
func WriteCache(path, inputDigest string, idx *Index) error {
	if idx == nil {
		return media.New(media.CatInvalidParams, media.StageIndex, "nil index")
	}
	if idx.InputDigest != inputDigest {
		return media.New(media.CatIndexInput, media.StageIndex,
			"index input digest %s != supplied %s", idx.InputDigest, inputDigest)
	}
	if idx.Version != Version {
		return media.New(media.CatIndexVersion, media.StageIndex,
			"refusing to cache index version %d (current %d)", idx.Version, Version)
	}
	env := &cacheEnvelope{
		CacheVersion: Version,
		IndexVersion: idx.Version,
		InputDigest:  inputDigest,
		IndexDigest:  idx.IndexDigest,
		Index:        idx,
	}
	env.EnvelopeHash = env.hash()
	data, err := json.Marshal(env)
	if err != nil {
		return media.Wrap(media.CatIO, media.StageIndex, err, "marshal cache envelope")
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return media.Wrap(media.CatIO, media.StageIndex, err, "create cache directory")
	}
	tmp := TempPath(path)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return media.Wrap(media.CatIO, media.StageIndex, err, "create temp cache")
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return media.Wrap(media.CatIO, media.StageIndex, err, "write temp cache")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return media.Wrap(media.CatIO, media.StageIndex, err, "sync temp cache")
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return media.Wrap(media.CatIO, media.StageIndex, err, "close temp cache")
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return media.Wrap(media.CatIO, media.StageIndex, err, "atomically replace cache")
	}
	return nil
}

// LoadCache attempts to reuse a cached index for inputDigest. On any mismatch
// or damage it returns a CacheReport explaining the reason and a nil index so
// the caller rebuilds. A temp file left by a crash is never accepted as a
// valid index: it is removed and reported.
func LoadCache(path, inputDigest string) (*Index, CacheReport) {
	rep := CacheReport{Path: path}
	tmp := TempPath(path)
	residue := ""
	if _, err := os.Stat(tmp); err == nil {
		// A crash left a partial temp file. Remove it unconditionally so it can
		// never be mistaken for the canonical index, and remember that it
		// happened even if the real cache turns out to be valid.
		residue = "temporary cache from an interrupted write was discarded"
		rep.Temp = tmp
		_ = os.Remove(tmp)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		rep.State = CacheMiss
		rep.Reason = "no cached index"
		if residue != "" {
			rep.State = CacheResidue
			rep.Reason = residue
			rep.Error = media.New(media.CatIndexTemp, media.StageIndex, residue)
		}
		return nil, rep
	}
	var env cacheEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		rep.State = CacheCorrupt
		rep.Reason = "cache envelope is not valid JSON"
		rep.Error = media.Wrap(media.CatIndexTruncated, media.StageIndex, err, rep.Reason)
		return nil, rep
	}
	if env.CacheVersion != Version || env.IndexVersion != Version {
		rep.State = CacheVersion
		rep.Reason = "cache written by a different index version"
		rep.Error = media.New(media.CatIndexVersion, media.StageIndex,
			"cache version %d index version %d, current %d", env.CacheVersion, env.IndexVersion, Version)
		return nil, rep
	}
	if env.EnvelopeHash != env.hash() {
		rep.State = CacheCorrupt
		rep.Reason = "cache envelope hash does not match its contents"
		rep.Error = media.New(media.CatIndexTruncated, media.StageIndex, rep.Reason)
		return nil, rep
	}
	if env.InputDigest != inputDigest {
		rep.State = CacheStale
		rep.Reason = "input digest changed since the index was written"
		rep.Error = media.New(media.CatIndexInput, media.StageIndex,
			"cached input %s != current %s", shortHash(env.InputDigest), shortHash(inputDigest))
		return nil, rep
	}
	if env.Index == nil || env.Index.IndexDigest != env.IndexDigest {
		rep.State = CacheCorrupt
		rep.Reason = "cached index digest does not match the envelope"
		rep.Error = media.New(media.CatIndexTruncated, media.StageIndex, rep.Reason)
		return nil, rep
	}
	if got := ComputeDigest(env.Index); got != env.IndexDigest {
		rep.State = CacheCorrupt
		rep.Reason = "cached entries do not reproduce the stored index digest"
		rep.Error = media.New(media.CatIndexTruncated, media.StageIndex, rep.Reason)
		return nil, rep
	}
	rep.State = CacheHit
	if residue != "" {
		rep.Reason = residue + "; the existing cache was still valid"
	}
	return env.Index, rep
}

// EnsureCache returns a usable index for inputDigest, reusing the cache when
// it is valid and rebuilding (then atomically caching) otherwise. The returned
// report always says which path was taken and why.
func EnsureCache(path, inputDigest string, build func() (*Index, error)) (*Index, CacheReport) {
	idx, rep := LoadCache(path, inputDigest)
	if idx != nil {
		return idx, rep
	}
	idx, err := build()
	if err != nil {
		rep.Rebuilt = false
		rep.Reason = strings.TrimSpace(rep.Reason + "; rebuild failed")
		if e, ok := media.AsError(err); ok {
			rep.Error = e
		}
		return idx, rep
	}
	if cerr := WriteCache(path, inputDigest, idx); cerr != nil {
		rep.Rebuilt = true
		rep.Reason = strings.TrimSpace(rep.Reason + "; rebuilt but cache write failed")
		if e, ok := media.AsError(cerr); ok {
			rep.Error = e
		}
		return idx, rep
	}
	rep.Rebuilt = true
	return idx, rep
}
