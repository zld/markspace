package ebml_test

import (
	"errors"
	"io"
	"testing"

	"mediad/internal/ebml"
	"mediad/internal/testkit"
)

func runDemux(t *testing.T, data []byte) (*ebml.Demuxer, []ebml.Block, error) {
	t.Helper()
	w := ebml.NewWalker(bytesReader(data), ebml.ReaderOptions{})
	d := ebml.NewDemuxer(w)
	var blocks []ebml.Block
	d.OnBlock = func(b ebml.Block) { blocks = append(blocks, b) }
	_, err := d.Run()
	return d, blocks, err
}

func TestDemuxSyntheticDocument(t *testing.T) {
	doc, err := testkit.BuildDoc(testkit.DefaultDocSpec())
	if err != nil {
		t.Fatal(err)
	}
	d, blocks, err := runDemux(t, doc.Data)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("demux error: %v", err)
	}
	if got, want := len(blocks), len(doc.Blocks); got != want {
		t.Fatalf("blocks = %d, want %d", got, want)
	}
	info := d.Info()
	if info.DocType != "matroska" {
		t.Fatalf("docType = %q", info.DocType)
	}
	if info.TimecodeScaleNs != 1_000_000 {
		t.Fatalf("scale = %d", info.TimecodeScaleNs)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(info.Tracks))
	}
	if info.Tracks[0].Type != 1 || info.Tracks[1].Type != 2 {
		t.Fatalf("track types = %d,%d", info.Tracks[0].Type, info.Tracks[1].Type)
	}
	if info.ClusterEvents == 0 {
		t.Fatal("no clusters seen")
	}

	// Every demuxed block must match ground truth on track, time and keyframe.
	for i, b := range blocks {
		want := doc.Blocks[i]
		if b.TrackNumber != want.Track {
			t.Fatalf("block %d track = %d want %d", i, b.TrackNumber, want.Track)
		}
		if b.TimecodeMs != want.TimeMs {
			t.Fatalf("block %d time = %d want %d", i, b.TimecodeMs, want.TimeMs)
		}
		if len(b.Payload) != want.PayloadLen {
			t.Fatalf("block %d payload = %d want %d", i, len(b.Payload), want.PayloadLen)
		}
		if b.TrackNumber == 1 && b.Keyframe != want.Keyframe {
			t.Fatalf("block %d keyframe = %v want %v", i, b.Keyframe, want.Keyframe)
		}
	}

	// Offsets must point at the actual block element in the file.
	for i, b := range blocks {
		if b.BlockOffset <= 0 || b.BlockOffset >= int64(len(doc.Data)) {
			t.Fatalf("block %d offset %d out of range", i, b.BlockOffset)
		}
		id := doc.Data[b.BlockOffset:]
		if len(id) < 1 || id[0] != 0xA3 {
			t.Fatalf("block %d offset %d does not point at a SimpleBlock (0x%02x)",
				i, b.BlockOffset, id[0])
		}
		if b.ClusterOffset >= b.BlockOffset {
			t.Fatalf("block %d cluster offset %d not before block %d",
				i, b.ClusterOffset, b.BlockOffset)
		}
	}
}

func TestDemuxTruncatedTailKeepsPrefix(t *testing.T) {
	doc, err := testkit.BuildDoc(testkit.DefaultDocSpec())
	if err != nil {
		t.Fatal(err)
	}
	// Cut in the last quarter so many blocks remain confirmed.
	cut := doc.Data[:len(doc.Data)*3/4]
	_, blocks, err := runDemux(t, cut)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("expected truncation, got %v", err)
	}
	if len(blocks) == 0 {
		t.Fatal("no blocks confirmed before truncation")
	}
	if len(blocks) >= len(doc.Blocks) {
		t.Fatalf("cut input produced all %d blocks", len(blocks))
	}
}
