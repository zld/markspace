package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"

	"mediatool/internal/timeline"
)

func TestStreamFragmentsDeliversEach(t *testing.T) {
	var stream []byte
	const n = 5
	for i := 0; i < n; i++ {
		spec := FragmentSpec{
			Seq: i, TrackID: 1, BaseMediaDecode: uint64(i) * 5760,
			SampleDurations: []uint32{3003, 2757},
			SampleSizes:     []uint32{4, 5},
			SamplePayload:   []byte("abcdefghi"),
		}
		stream = append(stream, BuildFragment(spec)...)
	}
	var seqs []int
	calls := 0
	err := StreamFromBytes(stream, func(frag *Fragment, fatal error) error {
		if fatal != nil {
			t.Fatalf("unexpected fatal: %v", fatal)
		}
		if frag == nil || frag.Rejected {
			t.Fatalf("valid fragment rejected: %+v", frag)
		}
		seqs = append(seqs, frag.Seq)
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != n {
		t.Fatalf("calls = %d, want %d", calls, n)
	}
	want := []int{0, 1, 2, 3, 4}
	for i := range want {
		if seqs[i] != want[i] {
			t.Fatalf("seq order wrong: %v", seqs)
		}
	}
}

func TestStreamIsolatesCorruptFragment(t *testing.T) {
	good := BuildFragment(FragmentSpec{
		Seq: 0, TrackID: 1, BaseMediaDecode: 0,
		SampleDurations: []uint32{100}, SampleSizes: []uint32{2},
		SamplePayload: []byte("ab"),
	})
	// A truncated box header at the end aborts scanning; the structural
	// error is delivered both via the callback and the return value.
	corrupt := []byte{0x00, 0x00, 0x00}
	stream := append(append([]byte{}, good...), corrupt...)

	delivered := 0
	sawFatal := false
	err := StreamFromBytes(stream, func(frag *Fragment, fatal error) error {
		if fatal != nil {
			sawFatal = true
			return nil
		}
		if frag != nil && !frag.Rejected {
			delivered++
		}
		return nil
	})
	if err == nil {
		t.Fatal("truncated header must surface a scan error")
	}
	if !sawFatal {
		t.Fatal("handler must receive the fatal error")
	}
	// The valid fragment before the corruption point is delivered; the
	// fatal structural error stops the stream rather than misparsing.
	if delivered != 1 {
		t.Fatalf("fragment before corruption must be delivered, got %d", delivered)
	}
}

func TestStreamPayloadBytesAvailable(t *testing.T) {
	payload := []byte("stream-payload")
	spec := FragmentSpec{
		Seq: 0, TrackID: 2, BaseMediaDecode: 0,
		SampleDurations: []uint32{1024},
		SampleSizes:     []uint32{uint32(len(payload))},
		SamplePayload:   payload,
	}
	err := StreamFromBytes(BuildFragment(spec), func(frag *Fragment, fatal error) error {
		if fatal != nil {
			t.Fatal(fatal)
		}
		if !bytes.Equal(frag.MdatPayload, payload) {
			t.Fatal("streamed payload mismatch")
		}
		b, _, perr := frag.SamplePayload(0, 0)
		if perr != nil || !bytes.Equal(b, payload) {
			t.Fatalf("sample slice unavailable: %v", perr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStreamBoundedByFragmentLimit(t *testing.T) {
	// A box declaring strictly more than MaxFragmentSize must be rejected
	// rather than buffered, proving the cap protects steady-state memory.
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header[0:4], uint32(MaxFragmentSize)+1)
	copy(header[4:], "mdat")
	// The size-limit check fires before the (missing) payload is read.
	stream := append(header, 1, 2, 3, 4)
	err := StreamFromBytes(stream, func(frag *Fragment, fatal error) error {
		return nil
	})
	me := timeline.AsMediaError(err)
	if me == nil || me.Class != timeline.ClassBoxLength {
		t.Fatalf("want box_length for oversize stream box, got %v", err)
	}
}
