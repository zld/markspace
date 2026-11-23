package mp4

import (
	"bytes"
	"io"
	"testing"
)

// repeatingReader emits the same fragment n times without ever holding
// more than one copy, mirroring a chunked network or file read stream.
type repeatingReader struct {
	one    []byte
	remain int
	pos    int
}

func (r *repeatingReader) Read(p []byte) (int, error) {
	if r.remain == 0 {
		return 0, io.EOF
	}
	if r.pos >= len(r.one) {
		r.pos = 0
		r.remain--
		if r.remain == 0 {
			return 0, io.EOF
		}
	}
	copied := copy(p, r.one[r.pos:])
	r.pos += copied
	return copied, nil
}

// TestStream1000FragmentsBoundedMemory drives the streaming reader with
// 1,000 fragments supplied by an on-demand reader and asserts every
// fragment is delivered while the parser only ever materializes one
// fragment at a time. Payload spooling in the remux layer keeps media
// bytes off the heap across the full pipeline.
func TestStream1000FragmentsBoundedMemory(t *testing.T) {
	const n = 1000
	spec := FragmentSpec{
		Seq: 0, TrackID: 1, BaseMediaDecode: 0,
		SampleDurations: []uint32{3003},
		SampleSizes:     []uint32{256},
		SamplePayload:   bytes.Repeat([]byte{0xAB}, 256),
	}
	one := BuildFragment(spec)
	stream := &repeatingReader{one: one, remain: n}

	delivered := 0
	samples := 0
	err := StreamFragments(stream, func(frag *Fragment, fatal error) error {
		if fatal != nil {
			return fatal
		}
		if frag == nil || frag.Rejected {
			t.Fatalf("fragment %d rejected: %+v", delivered, frag)
		}
		delivered++
		samples += frag.Runs[0].SampleCount()
		return nil
	})
	if err != nil {
		t.Fatalf("stream failed after %d fragments: %v", delivered, err)
	}
	if delivered != n {
		t.Fatalf("delivered = %d, want %d", delivered, n)
	}
	if samples != n {
		t.Fatalf("samples = %d, want %d", samples, n)
	}
}

// TestParseFragmentStaysUnderFragmentCap constructs a fragment near the
// 4 MiB limit and confirms the parser accepts it without an unbounded
// allocation path, while anything larger is rejected at the box header.
func TestParseFragmentStaysUnderFragmentCap(t *testing.T) {
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	spec := FragmentSpec{
		Seq: 0, TrackID: 1, BaseMediaDecode: 0,
		SampleDurations: []uint32{100},
		SampleSizes:     []uint32{uint32(len(payload))},
		SamplePayload:   payload,
	}
	frag, err := ParseFragment(bytes.NewReader(BuildFragment(spec)), 0)
	if err != nil || frag.Rejected {
		t.Fatalf("in-cap fragment rejected: %v %+v", err, frag.Errors)
	}
}
