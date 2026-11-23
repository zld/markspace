package ebml_test

import (
	"testing"

	"mediad/internal/ebml"
	"mediad/internal/merr"
	"mediad/internal/testkit"
)

func TestTruncatedLeafOutsideCluster(t *testing.T) {
	seg := testkit.Master(ebml.IDSegment,
		testkit.Master(ebml.IDInfo, testkit.Uint(ebml.IDTimecodeScale, 1)),
	)
	seg.UnknownSize = true
	root := testkit.Master(0x1A45DFA3, testkit.Str(ebml.IDDocType, "matroska"))
	root.Children = []*testkit.Element{seg}
	data, err := testkit.Serialize(root)
	if err != nil {
		t.Fatal(err)
	}
	// Cut inside the Info payload, outside any cluster.
	cut := data[:len(data)-1]
	_, err = collect(t, cut)
	if code := merr.CodeOf(err); code != merr.CodeTruncatedElement {
		t.Fatalf("code = %s want truncated_element (%v)", code, err)
	}
}

func TestOversizeElementRejected(t *testing.T) {
	root := testkit.Master(0x1A45DFA3,
		testkit.Bytes(ebml.IDSimpleBlock, make([]byte, 9<<20)),
	)
	data, err := testkit.Serialize(root)
	if err != nil {
		t.Fatal(err)
	}
	w := ebml.NewWalker(bytesReader(data), ebml.ReaderOptions{MaxElementSize: 8 << 20})
	var last error
	for {
		if _, err := w.Next(); err != nil {
			last = err
			break
		}
	}
	if code := merr.CodeOf(last); code != merr.CodeElementTooLarge {
		t.Fatalf("code = %s want element_too_large (%v)", code, last)
	}
}

func TestUnknownSizeLeafRejected(t *testing.T) {
	// Hand-build a SimpleBlock whose size is the unknown marker.
	var buf []byte
	buf = append(buf, testkit.IDBytes(ebml.IDSimpleBlock)...)
	buf = append(buf, testkit.UnknownSizeBytes...)
	buf = append(buf, 1, 2, 3)
	w := ebml.NewWalker(bytesReader(buf), ebml.ReaderOptions{})
	_, err := w.Next()
	if code := merr.CodeOf(err); code != merr.CodeUnknownSizeNotAllowed {
		t.Fatalf("code = %s want unknown_size_not_allowed (%v)", code, err)
	}
}

func TestUnknownSizeNestedOffsets(t *testing.T) {
	seg := testkit.Master(ebml.IDSegment)
	cl := testkit.Master(ebml.IDCluster,
		testkit.Uint(ebml.IDTimecode, 500),
		testkit.Bytes(ebml.IDSimpleBlock, []byte{0x81, 0, 0, 0x80, 0xAB}),
	)
	cl.UnknownSize = true
	seg.Children = []*testkit.Element{cl}
	seg.UnknownSize = true
	root := testkit.Master(0x1A45DFA3)
	root.Children = []*testkit.Element{seg}
	data, err := testkit.Serialize(root)
	if err != nil {
		t.Fatal(err)
	}
	wantCluster := testkit.OffsetsOf(root, ebml.IDCluster)
	wantBlock := testkit.OffsetsOf(root, ebml.IDSimpleBlock)
	evs, err := collect(t, data)
	if err != nil {
		t.Fatal(err)
	}
	var gotCluster, gotBlock []int64
	for _, ev := range evs {
		switch ev.ID {
		case ebml.IDCluster:
			if ev.Kind == ebml.Start {
				gotCluster = append(gotCluster, ev.HeaderOffset)
			}
		case ebml.IDSimpleBlock:
			gotBlock = append(gotBlock, ev.HeaderOffset)
		}
	}
	if len(gotCluster) != 1 || gotCluster[0] != wantCluster[0] {
		t.Fatalf("cluster offset = %v want %v", gotCluster, wantCluster)
	}
	if len(gotBlock) != 1 || gotBlock[0] != wantBlock[0] {
		t.Fatalf("block offset = %v want %v", gotBlock, wantBlock)
	}
}
