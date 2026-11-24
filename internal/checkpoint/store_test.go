package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.ckpt")
	st := &State{
		JobID:          "job-1",
		InputPath:      "/tmp/in.pcap",
		ConfigSummary:  SummaryHash([]byte(`{"rate":48000}`)),
		PrefixHash:     PrefixHashOf([]byte("abcdef")),
		ConsumedOffset: 6,
		NextSeq:        42,
		BlocksDone:     3,
		OutputSamples:  1440,
		OutputHash:     "deadbeef",
	}
	if err := Save(path, st); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Version != Version || got.NextSeq != 42 || got.ConfigSummary != st.ConfigSummary {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	// No temp files left behind after the atomic rename.
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("dir contains %d entries, want 1", len(entries))
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.ckpt")); !os.IsNotExist(err) {
		t.Fatalf("want not-exist, got %v", err)
	}
}

func TestVerifyConfigMismatch(t *testing.T) {
	st := &State{
		ConfigSummary:  SummaryHash([]byte(`{"rate":48000}`)),
		PrefixHash:     PrefixHashOf([]byte("abcdef")),
		ConsumedOffset: 6,
	}
	prefix := []byte("abcdef")
	err := st.Verify([]byte(`{"rate":24000}`), prefix, prefix)
	var me *MismatchError
	if !errors.As(err, &me) || !errors.Is(err, ErrConfigMismatch) {
		t.Fatalf("want config mismatch, got %v", err)
	}
	if me.Offset != 6 {
		t.Fatalf("offset = %d, want 6", me.Offset)
	}
}

func TestVerifyPrefixMismatchOffset(t *testing.T) {
	committed := []byte("abcdefgh")
	corrupt := []byte("abcdxfgh") // divergence at byte 4
	st := &State{
		ConfigSummary:  SummaryHash([]byte("cfg")),
		PrefixHash:     PrefixHashOf(committed),
		ConsumedOffset: int64(len(committed)),
	}
	err := st.Verify([]byte("cfg"), committed, corrupt)
	var me *MismatchError
	if !errors.As(err, &me) || !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("want prefix mismatch, got %v", err)
	}
	if me.Offset != 4 {
		t.Fatalf("diff offset = %d, want 4", me.Offset)
	}
	// Identical prefix and config must verify cleanly.
	if err := st.Verify([]byte("cfg"), committed, committed); err != nil {
		t.Fatalf("clean verify failed: %v", err)
	}
}

func TestFirstDiff(t *testing.T) {
	if off := FirstDiff([]byte("abc"), []byte("abd")); off != 2 {
		t.Fatalf("off = %d, want 2", off)
	}
	if off := FirstDiff([]byte("abc"), []byte("ab")); off != 2 {
		t.Fatalf("prefix-length off = %d, want 2", off)
	}
}
