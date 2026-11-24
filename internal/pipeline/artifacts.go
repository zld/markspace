package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	idxpkg "media-archiver/internal/index"
	"media-archiver/internal/media"
)

const (
	parseReportPath     = "parse/parse-report.json"
	canonicalBlocksPath = "transform/canonical-blocks.json"
	canonicalPCMPath    = "transform/canonical.pcm"
	provenancePath      = "transform/provenance.json"
	indexPath           = "index/index.json"
	indexChecksumPath   = "index/index.sha256"
	statsPath           = "verify/stats.json"
)

func artifactPath(workdir, rel string) string { return filepath.Join(workdir, rel) }

func writeJSONArtifact(workdir, rel string, v any) (string, error) {
	data, err := media.MarshalCanonicalJSON(v)
	if err != nil {
		return "", err
	}
	path := artifactPath(workdir, rel)
	if err := media.WriteFileAtomic(path, data, 0o644); err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func writeBytesArtifact(workdir, rel string, data []byte) (string, error) {
	path := artifactPath(workdir, rel)
	if err := media.WriteFileAtomic(path, data, 0o644); err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func loadCanonicalBlocks(workdir string, frameBytes int) ([]media.CanonicalBlock, []byte, error) {
	blockData, err := os.ReadFile(artifactPath(workdir, canonicalBlocksPath))
	if err != nil {
		return nil, nil, err
	}
	var blocks []media.CanonicalBlock
	if err := json.Unmarshal(blockData, &blocks); err != nil {
		return nil, nil, media.NewError(media.CodeInternal, media.StageTransform, -1, "parse canonical blocks", err)
	}
	pcm, err := os.ReadFile(artifactPath(workdir, canonicalPCMPath))
	if err != nil {
		return nil, nil, err
	}
	var off int
	for i := range blocks {
		n := int(blocks[i].FrameCount) * frameBytes
		if off+n > len(pcm) {
			return nil, nil, media.NewError(media.CodeValidation, media.StageTransform, blocks[i].OriginalOffset,
				"canonical PCM artifact shorter than block metadata", nil)
		}
		blocks[i].PCM = pcm[off : off+n]
		if blocks[i].AudioSHA256 != media.SHA256Hex(blocks[i].PCM) {
			return nil, nil, media.NewError(media.CodeValidation, media.StageTransform, blocks[i].OriginalOffset,
				"canonical block digest mismatch", nil)
		}
		off += n
	}
	if off != len(pcm) {
		return nil, nil, media.NewError(media.CodeValidation, media.StageTransform, -1,
			fmt.Sprintf("canonical PCM length mismatch: consumed %d of %d", off, len(pcm)), nil)
	}
	return blocks, pcm, nil
}

func writeIndexArtifact(workdir string, idx *idxpkg.Index) (string, error) {
	data, err := idx.CanonicalJSON()
	if err != nil {
		return "", err
	}
	if err := media.WriteFileAtomic(artifactPath(workdir, indexPath), data, 0o644); err != nil {
		return "", err
	}
	sha := hashBytes(data)
	checksum := fmt.Sprintf("%s  index.json\n", sha)
	if err := media.WriteFileAtomic(artifactPath(workdir, indexChecksumPath), []byte(checksum), 0o644); err != nil {
		return "", err
	}
	return sha, nil
}

func loadIndexArtifact(workdir, expectedSHA string) (*idxpkg.Index, string, error) {
	path := artifactPath(workdir, indexPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", media.NewError(media.CodeIndexCorrupt, media.StageIndex, -1, "index missing", err)
	}
	sha := hashBytes(data)
	if expectedSHA != "" && sha != expectedSHA {
		return nil, sha, media.NewError(media.CodeIndexCorrupt, media.StageIndex, -1, "index checksum mismatch", nil)
	}
	idx, err := idxpkg.Load(path)
	if err != nil {
		return nil, sha, err
	}
	return idx, sha, nil
}

func removeArtifact(workdir, rel string) {
	_ = os.Remove(artifactPath(workdir, rel))
}
