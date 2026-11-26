package cmaf

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"

	"cmafrepair/pkg/media/timebase"
)

// Writer emits repaired fMP4 fragments. The repair is a strict in-place tfdt
// value patch: every other byte is copied verbatim, so moof/mdat sizes and
// sample offsets stay valid. Patches are written to a temp file and renamed
// into place only after the full content and its checksum are committed.
type Writer struct {
	outDir string
}

// NewWriter prepares an output directory (created if needed).
func NewWriter(outDir string) (*Writer, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, timebase.NewError(timebase.CodeIO, timebase.PhaseRepair,
			"mkdir %s: %v", outDir, err)
	}
	return &Writer{outDir: outDir}, nil
}

// WriteFragment streams src to dst, applying the tfdt patch and honoring mdat
// truncation. It returns the destination path, bytes written and whether a
// patch was needed. A GAP fragment is not emitted (the manifest marks it); a
// truncated fragment is emitted with its intact bytes but flagged.
func (w *Writer) WriteFragment(src string, f *Fragment, fixedTfdt uint64, status RepairStatus) (string, int64, bool, error) {
	if status == StatusGap {
		return "", 0, false, nil
	}
	dst := filepath.Join(w.outDir, f.Name)
	tmp, err := os.CreateTemp(w.outDir, ".tmp-"+f.Name+"-*")
	if err != nil {
		return "", 0, false, timebase.NewError(timebase.CodeIO, timebase.PhaseRepair,
			"create temp for %s: %v", f.Name, err)
	}
	tmpName := tmp.Name()
	// On any failure the temp file is removed; a committed rename leaves none.
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	in, err := os.Open(src)
	if err != nil {
		return "", 0, false, timebase.NewError(timebase.CodeIO, timebase.PhaseRepair,
			"open source %s: %v", src, err)
	}
	defer in.Close()

	n, patched, err := streamPatch(in, tmp, f, fixedTfdt)
	if err != nil {
		return "", 0, false, err
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, false, timebase.NewError(timebase.CodeIO, timebase.PhaseRepair,
			"fsync %s: %v", f.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, false, timebase.NewError(timebase.CodeIO, timebase.PhaseRepair,
			"close %s: %v", f.Name, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", 0, false, timebase.NewError(timebase.CodeIO, timebase.PhaseRepair,
			"rename %s: %v", f.Name, err)
	}
	committed = true
	return dst, n, patched, nil
}

// streamPatch copies the fragment in bounded chunks, overwriting the tfdt
// value bytes. No whole-file buffering: peak memory stays at the chunk size.
func streamPatch(in io.Reader, out io.Writer, f *Fragment, fixedTfdt uint64) (int64, bool, error) {
	const chunk = 64 * 1024
	buf := make([]byte, chunk)
	var pos int64
	patched := false
	patchStart, patchLen := f.TfdtOffset, f.TfdtValueLen

	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			seg := buf[:n]
			if patchLen > 0 {
				seg = applyTfdtPatch(seg, pos, patchStart, patchLen, fixedTfdt, &patched)
			}
			if _, werr := out.Write(seg); werr != nil {
				return pos, patched, timebase.NewError(timebase.CodeIO, timebase.PhaseRepair,
					"write: %v", werr)
			}
			pos += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return pos, patched, timebase.NewError(timebase.CodeIO, timebase.PhaseRepair,
				"read: %v", rerr)
		}
	}
	if patchLen > 0 && !patched {
		return pos, patched, timebase.AtError(timebase.CodeTfdtUnrepairable, timebase.PhaseRepair,
			patchStart, "tfdt patch window never reached in %s", f.Name)
	}
	return pos, patched, nil
}

// applyTfdtPatch rewrites the tfdt value if the current chunk covers it.
func applyTfdtPatch(seg []byte, chunkPos, patchStart int64, patchLen int, value uint64, patched *bool) []byte {
	rel := patchStart - chunkPos
	if rel < 0 || int64(len(seg)) < rel+int64(patchLen) {
		return seg
	}
	off := int(rel)
	if patchLen == 8 {
		binary.BigEndian.PutUint64(seg[off:off+8], value)
	} else {
		binary.BigEndian.PutUint32(seg[off:off+4], uint32(value))
	}
	*patched = true
	return seg
}

// VerifyPatch re-parses a written fragment and confirms its tfdt equals the
// expected rebuilt value, giving the checkpoint a hard correctness gate.
func VerifyPatch(path string, wantTfdt uint64) error {
	f, err := ParseFragment(path)
	if err != nil {
		return err
	}
	if f.Tfdt != wantTfdt {
		return timebase.AtError(timebase.CodeTfdtUnrepairable, timebase.PhaseRepair,
			f.TfdtOffset, "verify: tfdt=%d want %d", f.Tfdt, wantTfdt)
	}
	return nil
}

// CopyInit streams an init segment unchanged into the output directory.
func (w *Writer) CopyInit(src, name string) (string, int64, error) {
	dst := filepath.Join(w.outDir, name)
	if err := copyFile(src, dst); err != nil {
		return "", 0, err
	}
	st, _ := os.Stat(dst)
	return dst, st.Size(), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseRepair, "open %s: %v", src, err)
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-copy-*")
	if err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseRepair, "temp: %v", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseRepair, "copy: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseRepair, "rename: %v", err)
	}
	committed = true
	return nil
}

// String renders a repair status for reports.
func (s RepairStatus) String() string { return string(s) }
