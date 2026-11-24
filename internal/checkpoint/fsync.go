package checkpoint

import (
	"io"
	"os"
)

func copySync(w io.Writer, r io.Reader) (int64, error) { return io.Copy(w, r) }

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
