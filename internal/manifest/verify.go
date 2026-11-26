package manifest

import (
	"fmt"
	"sort"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/model"
	"example.com/mediavault/internal/packageindex"
	"example.com/mediavault/internal/store"
)

// Result is the outcome of an independent verification run.
type Result struct {
	Verified int      `json:"verified"`
	Failed   int      `json:"failed"`
	Digest   string   `json:"digest"`
	Failures []string `json:"failures,omitempty"`
	Entries  []string `json:"entries,omitempty"`
}

// Verifier re-checks each archived entry against the object store without
// trusting any pipeline state.
type Verifier struct {
	Store *store.Store
}

// Verify re-hashes every manifest entry and every referenced block. It also
// re-scans the stored MP4 archive so that a corrupt wrapper fails the manifest.
func (v *Verifier) Verify(f *File) (*Result, error) {
	if v.Store == nil {
		return nil, fault.New(fault.CodeManifest, fault.StageArchived, -1, "verifier needs a store")
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	res := &Result{Digest: f.Digest}
	for _, e := range f.Entries {
		if err := v.verifyEntry(e); err != nil {
			res.Failed++
			res.Failures = append(res.Failures, fmt.Sprintf("%s: %v", e.Key, err))
			continue
		}
		res.Verified++
		res.Entries = append(res.Entries, e.Key)
	}
	sort.Strings(res.Entries)
	return res, nil
}

func (v *Verifier) verifyEntry(e Entry) error {
	if e.ArchiveDigest == "" {
		return fault.New(fault.CodeManifest, fault.StageArchived, -1, "missing archive digest")
	}
	if err := v.Store.VerifyObject(e.ArchiveDigest, e.ArchiveSize); err != nil {
		return err
	}
	if e.SourceDigest != "" {
		if err := v.Store.VerifyObject(e.SourceDigest, -1); err != nil {
			return err
		}
	}
	for _, c := range e.Chunks {
		if c.Length < 0 || c.Length > 1<<20 {
			return fault.New(fault.CodeManifest, fault.StageArchived, -1, "block %d has implausible length %d", c.Index, c.Length)
		}
		if err := v.Store.VerifyObject(c.Digest, c.Length); err != nil {
			return err
		}
	}
	if err := v.verifyArchiveWrapper(e); err != nil {
		return err
	}
	return nil
}

// verifyArchiveWrapper re-parses the archive MP4 and checks the container
// digest recorded at seal time plus the internal box extents.
func (v *Verifier) verifyArchiveWrapper(e Entry) error {
	path, err := v.Store.Path(e.ArchiveDigest)
	if err != nil {
		return err
	}
	idx, err := packageindex.Scan(path)
	if err != nil {
		return err
	}
	if idx.ContainerDigest != e.ContainerDigest {
		return fault.New(fault.CodeManifest, fault.StageArchived, -1,
			"archive container digest %s does not match manifest %s", idx.ContainerDigest, e.ContainerDigest)
	}
	f, err := v.Store.OpenObject(e.ArchiveDigest)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := packageindex.ValidateChunkOffsets(f, idx); err != nil {
		return err
	}
	return nil
}

// VerifySources checks that an entry's content digest still describes the
// blocks listed, independent of stored block bytes. This is the "manifest
// alone" audit used by tests and the verify command.
func VerifySources(entries []Entry) error {
	for _, e := range entries {
		if e.Blocks != len(e.Chunks) {
			return fault.New(fault.CodeManifest, fault.StageArchived, -1,
				"%s: %d blocks declared, %d chunks present", e.Key, e.Blocks, len(e.Chunks))
		}
		if e.Frames <= 0 && e.Transport == string(model.TransportWAV) {
			return fault.New(fault.CodeManifest, fault.StageArchived, -1, "%s: zero frames", e.Key)
		}
		next := 0
		for _, c := range e.Chunks {
			if c.Index != next {
				return fault.New(fault.CodeManifest, fault.StageArchived, -1,
					"%s: block index %d out of order, expected %d", e.Key, c.Index, next)
			}
			next++
		}
	}
	return nil
}
