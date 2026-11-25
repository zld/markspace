package repack

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediaflow/internal/index"
)

func assembleOutput(outputPath, chunkDir string, chunks []index.ChunkRecord) (string, int64, error) {
	tmp := outputPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	w := io.MultiWriter(out, h)

	// Use the pinned MP4 library to write the standard file-type box. Chunk boxes
	// are deterministic length-prefixed mchk boxes appended after ftyp.
	_, err = mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:   mp4.StrToBoxType("isom"),
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: mp4.StrToBoxType("isom")},
			{CompatibleBrand: mp4.StrToBoxType("mp41")},
		},
	}, mp4.Context{})
	if err != nil {
		out.Close()
		return "", 0, err
	}
	var size int64
	for _, rec := range chunks {
		if rec.Status != index.StatusComplete {
			out.Close()
			return "", 0, os.ErrInvalid
		}
		path := chunkPath(chunkDir, rec.Index, false)
		f, err := os.Open(path)
		if err != nil {
			out.Close()
			return "", 0, err
		}
		n, err := io.Copy(w, f)
		f.Close()
		if err != nil {
			out.Close()
			return "", 0, err
		}
		size += n
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return "", 0, err
	}
	if err := out.Close(); err != nil {
		return "", 0, err
	}
	info, err := os.Stat(tmp)
	if err != nil {
		return "", 0, err
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if err := os.Rename(tmp, outputPath); err != nil {
		return "", 0, err
	}
	return digest, info.Size(), nil
}

func verifyOutput(path, expected string) (bool, error) {
	if expected == "" {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return "sha256:"+hex.EncodeToString(h.Sum(nil)) == expected, nil
}
