package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"releasepipe/internal/errs"
)

// BlockSize is the fixed number of asset records per checkpoint block.
const BlockSize = 2

type checkpointFile struct {
	LastCompleteBlock int      `json:"last_complete_block"`
	BlockDigests      []string `json:"block_digests"`
}

type blockFile struct {
	Records []AssetRecord `json:"records"`
	SHA256  string        `json:"sha256"`
}

// BlockDigest digests a block's records in canonical JSON form.
func BlockDigest(recs []AssetRecord) string {
	raw, _ := json.Marshal(recs) // struct field order is deterministic
	return HashBytes(raw)
}

func blockPath(dir string, n int) string {
	return filepath.Join(dir, "blocks", fmt.Sprintf("block-%04d.json", n))
}

// AppendBlock persists block n: the data block is written and synced
// first, and only then is the checkpoint committed. A crash between the
// two leaves an unreferenced block file, never a dangling checkpoint.
func AppendBlock(dir string, n int, recs []AssetRecord) error {
	digest := BlockDigest(recs)
	bf := blockFile{Records: recs, SHA256: digest}
	if err := atomicWriteJSON(filepath.Join(dir, "blocks"),
		fmt.Sprintf("block-%04d.json", n), bf); err != nil {
		return err
	}
	cp := checkpointFile{LastCompleteBlock: -1}
	raw, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err == nil {
		if err := json.Unmarshal(raw, &cp); err != nil {
			return errs.New(errs.StageIndex, errs.CodeChecksum, dir, 0,
				"checkpoint.json corrupt: %v", err)
		}
	} else if !os.IsNotExist(err) {
		return errs.Wrap(errs.StageIndex, errs.CodeInternal, dir, -1, err)
	}
	for len(cp.BlockDigests) <= n {
		cp.BlockDigests = append(cp.BlockDigests, "")
	}
	cp.BlockDigests[n] = digest
	cp.LastCompleteBlock = n
	return atomicWriteJSON(dir, "checkpoint.json", cp)
}

// LoadCheckpoint verifies every block digest and returns the records of
// the longest valid prefix plus the index of the block to resume from.
// A corrupt or missing block truncates the run at that point.
func LoadCheckpoint(dir string) (recs []AssetRecord, nextBlock int, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, errs.Wrap(errs.StageIndex, errs.CodeInternal, dir, -1, err)
	}
	var cp checkpointFile
	if err := json.Unmarshal(raw, &cp); err != nil {
		// A torn checkpoint write is not fatal: restart the run.
		return nil, 0, nil
	}
	for n := 0; n <= cp.LastCompleteBlock; n++ {
		raw, err := os.ReadFile(blockPath(dir, n))
		if err != nil {
			return recs, n, nil // missing data block: resume here
		}
		var bf blockFile
		if err := json.Unmarshal(raw, &bf); err != nil {
			return recs, n, nil
		}
		if n >= len(cp.BlockDigests) || bf.SHA256 != cp.BlockDigests[n] ||
			BlockDigest(bf.Records) != bf.SHA256 {
			return recs, n, nil // digest mismatch: discard from here on
		}
		recs = append(recs, bf.Records...)
	}
	return recs, cp.LastCompleteBlock + 1, nil
}
