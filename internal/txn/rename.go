package txn

import (
	"io"
	"os"
)

// atomicRename moves src to dst atomically. On the same filesystem os.Rename
// is atomic; if the temp dir and target ever land on different devices it
// falls back to writing a sibling temp and replacing it, with fsync ordering
// so a crash never exposes a partially written destination.
func atomicRename(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return syncDir(dst)
	} else if !isCrossDevice(err) {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".swap"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, privateFileMode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Remove(src)
	return syncDir(dst)
}

func syncDir(path string) error {
	dir, err := os.Open(pathDir(path))
	if err != nil {
		return nil // fsync of the directory is best-effort on some platforms
	}
	defer dir.Close()
	return dir.Sync()
}
