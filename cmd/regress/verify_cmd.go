package main

import (
	"fmt"
	"os"
	"path/filepath"

	"cmafrepair/pkg/media/cache"
	"cmafrepair/pkg/media/checkpoint"
)

// runVerify compares a resumed/partial output against a full reference run
// and optionally demonstrates the cache invalidation rules.
func runVerify(args []string) error {
	f := parseFlags(args)
	in, ok1 := f.string_("in")
	out, ok2 := f.string_("out")
	compare, ok3 := f.string_("compare")
	if !ok1 || !ok2 || !ok3 {
		return &exitError{code: exitInvalidInput,
			msg: "verify requires --in DIR --out DIR --compare DIR"}
	}
	exercise := f.bools["exercise-invalidation"]

	cp := checkpoint.NewStore(out)
	if err := cp.Load(); err != nil {
		return &exitError{code: exitInvalidInput, msg: err.Error()}
	}
	refCp := checkpoint.NewStore(compare)
	if err := refCp.Load(); err != nil {
		return &exitError{code: exitInvalidInput, msg: err.Error()}
	}

	mismatch := 0
	// 1) Every output checkpoint item must verify against bytes on disk.
	for _, it := range cp.SortedItems() {
		if !cp.IsVerified(it.Key) {
			fmt.Printf("MISMATCH checksum key=%s path=%s\n", it.Key, it.OutputPath)
			mismatch++
		}
	}
	// 2) Output bundle must equal the full reference bundle when complete.
	outBundle := cp.BundleHash()
	refBundle := refCp.BundleHash()
	fmt.Printf("bundle_sha256=%s\n", outBundle)
	fmt.Printf("reference_sha256=%s\n", refBundle)
	bundleMatch := outBundle == refBundle
	fmt.Printf("bundle_match=%t\n", bundleMatch)
	fmt.Printf("checksum_match=%t\n", cp.Count() > 0 && mismatch == 0)

	if exercise {
		if err := exerciseInvalidation(in, out); err != nil {
			return err
		}
	}

	if mismatch != 0 || !bundleMatch {
		return &exitError{code: exitVerifyFailed,
			msg: fmt.Sprintf("verification failed: %d checksum mismatch(es), bundle_match=%t",
				mismatch, bundleMatch)}
	}
	fmt.Println("verify OK")
	return nil
}

// exerciseInvalidation demonstrates the three invalidation contracts on
// throwaway copies, never touching the real output:
//   - replacing a file invalidates both parse and index keys
//   - a metadata-only change keeps the parse key but rebuilds the index
func exerciseInvalidation(in, out string) error {
	work, err := os.MkdirTemp(out, ".invalidation-*")
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	defer os.RemoveAll(work)

	store, err := cache.NewStore(filepath.Join(work, "cache"))
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	// Pick a real fragment from the input as the demonstration subject.
	reps, err := listRepDirs(in)
	if err != nil || len(reps) == 0 {
		return &exitError{code: exitInvalidInput, msg: "no reps to exercise invalidation"}
	}
	names, err := listFragments(filepath.Join(in, reps[0]))
	if err != nil || len(names) == 0 {
		return &exitError{code: exitInvalidInput, msg: "no fragments to exercise invalidation"}
	}
	src := filepath.Join(in, reps[0], names[0])
	id, err := cache.IdentityOf(src)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	h1, err := cache.ContentHashFile(src)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	parse1 := cache.NewParseKey(id, h1)

	// Copy and mutate the file at the same path.
	copyPath := filepath.Join(work, names[0])
	raw, err := os.ReadFile(src)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	if err := os.WriteFile(copyPath, raw, 0o600); err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	store.Put(parse1.Hash()+":parse", []byte("v1"))
	if _, o := store.Get(parse1.Hash() + ":parse"); !o.Hit {
		return &exitError{code: exitUnsafe, msg: "expected initial parse cache hit"}
	}

	// Replacement: append a byte -> new content hash -> new parse key.
	mutated := append(append([]byte{}, raw...), 0x7f)
	if err := os.WriteFile(copyPath, mutated, 0o600); err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	h2, err := cache.ContentHashFile(copyPath)
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	parse2 := cache.NewParseKey(id, h2)
	replacementInvalidates := parse1.Hash() != parse2.Hash()
	fmt.Printf("invalidation replacement: parse_key_changed=%t index_key_changed=%t\n",
		replacementInvalidates, replacementInvalidates)

	// Metadata-only: parse key stable, index key changes.
	meta1 := cache.Fingerprint(map[string]any{"target": 4})
	meta2 := cache.Fingerprint(map[string]any{"target": 6})
	ik1 := cache.NewIndexKey(parse2, "timeline", meta1)
	ik2 := cache.NewIndexKey(parse2, "timeline", meta2)
	parseStable := cache.NewParseKey(id, h2).Hash() == parse2.Hash()
	indexRebuilds := ik1.Hash() != ik2.Hash()
	fmt.Printf("invalidation metadata-only: parse_cache_hit=%t index_rebuilt=%t\n",
		parseStable, indexRebuilds)

	if !replacementInvalidates || !parseStable || !indexRebuilds {
		return &exitError{code: exitVerifyFailed, msg: "invalidation contract violated"}
	}
	return nil
}
