package cmaf_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"example.com/archiveverify/internal/cmaf"
	"example.com/archiveverify/internal/fixture"
	"example.com/archiveverify/internal/model"
)

func TestScanFixtureBoxesAndDurations(t *testing.T) {
	ws, err := fixture.Generate(t.TempDir(), "clean")
	if err != nil {
		t.Fatal(err)
	}
	init, err := cmaf.ParseInitFile(filepath.Join(ws.Dir, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if len(init.Tracks) != 2 || init.Track(1).TimeScale != 30_000 || init.Track(2).TimeScale != 48_000 {
		t.Fatalf("bad init tracks: %+v", init.Tracks)
	}

	frag, err := cmaf.ParseFragmentFile(filepath.Join(ws.Dir, "fragments", "seg-2.m4s"), "src-a", init)
	if err != nil {
		t.Fatal(err)
	}
	if !frag.HasSequence || frag.Sequence != 3 {
		t.Fatalf("mfhd sequence=%d ok=%v", frag.Sequence, frag.HasSequence)
	}
	if got := frag.TotalDuration(1); got != 30_000 {
		t.Fatalf("video duration=%d want 30000", got)
	}
	if got := frag.TotalDuration(2); got != 48_000 {
		t.Fatalf("audio duration=%d want 48000", got)
	}
	if len(frag.Tracks[0].Samples) != 30 || !frag.Tracks[0].Samples[0].Sync {
		t.Fatalf("video samples/keyframe wrong: %+v", frag.Tracks[0])
	}
	if frag.Mdat == nil || frag.Tracks[0].Samples[0].ByteOffset != frag.Mdat.PayloadOffset {
		t.Fatalf("first video byte offset=%d mdat payload=%+v",
			frag.Tracks[0].Samples[0].ByteOffset, frag.Mdat)
	}
	if got := frag.Tracks[1].Samples[0].ByteOffset - frag.Mdat.PayloadOffset; got != 30*16 {
		t.Fatalf("audio payload offset relative mdat=%d want %d", got, 30*16)
	}
}

func TestScanRejectsTruncatedBoxWithOffset(t *testing.T) {
	ws, err := fixture.Generate(t.TempDir(), "faults")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ws.Dir, "fragments", "seg-truncated.m4s")
	_, err = cmaf.ParseFragmentFile(path, "fault-src", nil)
	var me *model.MediaError
	if !errors.As(err, &me) || me.Code != model.CodeParse {
		t.Fatalf("expected parse MediaError, got %v", err)
	}
	st, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if me.FirstUnreadable != st.Size() {
		t.Fatalf("FirstUnreadable=%d want EOF %d; box=%s offset=%d",
			me.FirstUnreadable, st.Size(), me.BoxType, me.BoxOffset)
	}
}

func TestReadBoxAtRejectsOversizeAndPartialHeader(t *testing.T) {
	// Declared size is exactly the admitted input size, but one byte over the
	// box limit; no payload read is required to reject it.
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(cmaf.MaxBoxSize+1))
	copy(hdr[4:8], "big ")
	size := cmaf.MaxBoxSize + 1
	_, err := cmaf.ReadBoxAt(bytes.NewReader(hdr[:]), "oversize", 0, size, cmaf.MaxBoxSize)
	var me *model.MediaError
	if !errors.As(err, &me) || me.Code != model.CodeLimit {
		t.Fatalf("expected limit MediaError, got %v", err)
	}

	_, err = cmaf.ReadBoxAt(bytes.NewReader(hdr[:4]), "partial", 0, 4, cmaf.MaxBoxSize)
	if !errors.As(err, &me) || me.FirstUnreadable != 4 {
		t.Fatalf("expected partial header offset 4, got %v", err)
	}
}
