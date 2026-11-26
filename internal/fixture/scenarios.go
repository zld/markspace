package fixture

import "os"

// ReplaceWithSameBytes rewrites path through a temp file and rename. Content
// SHA-256 stays identical while device/inode identity changes.
func ReplaceWithSameBytes(path string, opts Options) error {
	b, err := Build(opts)
	if err != nil {
		return err
	}
	return atomicWrite(path, b)
}

// TruncateCluster cuts the tail of the final cluster/block. The generated
// PCM blocks are much larger than the removed tail, so the file ends inside a
// SimpleBlock and exercises truncated-cluster diagnostics.
func TruncateCluster(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	cut := st.Size() - 37
	if cut <= 0 {
		cut = st.Size() / 2
	}
	return os.Truncate(path, cut)
}
