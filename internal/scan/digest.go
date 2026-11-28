package scan

import (
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"

	"avpipe/internal/model"
)

// readChunk bounds the amount of data held while hashing one file.
const readChunk = 64 * 1024

// HashBytes returns the stable FNV-1a 64-bit hex digest of a byte slice. The
// same algorithm is used for inputs and generated outputs so a changed input
// and a changed output are both detectable with one primitive.
func HashBytes(data []byte) string {
	h := fnv.New64a()
	h.Write(data)
	return sumHex(h.Sum64())
}

// FileDigest streams one file through the hash without loading it whole, and
// rejects any declared access beyond the real file size.
func FileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, model.NewError(model.CodeIO, false, "open %s: %v", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", 0, model.NewError(model.CodeIO, false, "stat %s: %v", path, err)
	}
	h := fnv.New64a()
	buf := make([]byte, readChunk)
	var seen int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			seen += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, model.NewError(model.CodeIO, false, "read %s: %v", path, err)
		}
	}
	if seen != info.Size() {
		return "", 0, model.NewError(model.CodeOffsetOutOfRange, false,
			"short read %s: got %d want %d", path, seen, info.Size())
	}
	return sumHex(h.Sum64()), info.Size(), nil
}

// CaseDigest combines the MP4 index and every per-track RTP file into one
// digest. Track ids are sorted first so the digest is order independent.
func CaseDigest(dir string, desc model.CaseDesc) (string, map[string]int64, error) {
	h := fnv.New64a()
	sizes := map[string]int64{}

	write := func(name, digest string, size int64) {
		io.WriteString(h, name)
		io.WriteString(h, ":")
		io.WriteString(h, digest)
		io.WriteString(h, "\n")
		sizes[name] = size
	}

	mp4Digest, mp4Size, err := FileDigest(filepath.Join(dir, model.IndexMP4))
	if err != nil {
		return "", nil, err
	}
	write(model.IndexMP4, mp4Digest, mp4Size)

	ids := make([]int, 0, len(desc.Tracks))
	for _, td := range desc.Tracks {
		ids = append(ids, td.ID)
	}
	sort.Ints(ids)
	for _, id := range ids {
		name := model.RTPPath(id)
		d, size, err := FileDigest(filepath.Join(dir, name))
		if err != nil {
			return "", nil, err
		}
		write(name, d, size)
	}
	return sumHex(h.Sum64()), sizes, nil
}

func sumHex(v uint64) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		out[i] = digits[v&0xf]
		v >>= 4
	}
	return string(out)
}
