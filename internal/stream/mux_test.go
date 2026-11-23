package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// trivialFeed emits pre-scripted items.
type trivialFeed struct {
	items []Item
	err   error
}

func (f *trivialFeed) Next(ctx context.Context) (Item, error) {
	if len(f.items) == 0 {
		if f.err != nil {
			return Item{}, f.err
		}
		return Item{}, io.EOF
	}
	it := f.items[0]
	f.items = f.items[1:]
	return it, nil
}

func TestMuxDemuxRoundTrip(t *testing.T) {
	low := bytes.Repeat([]byte("ab"), 40)
	high := bytes.Repeat([]byte("xyz"), 30)
	feed := &trivialFeed{items: []Item{
		{Layer: "low", Chunk: Chunk{Offset: 0, Data: low[:50]}},
		{Layer: "high", Chunk: Chunk{Offset: 0, Data: high[:40]}},
		{Layer: "low", Chunk: Chunk{Offset: 50, Data: low[50:]}},
		{Layer: "high", Chunk: Chunk{Offset: 40, Data: high[40:]}},
	}}
	mux, err := NewMuxSource(feed, []string{"low", "high"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(mux, Options{ChunkBytes: 7, Queue: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d, err := NewDemux(r, []string{"low", "high"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{}
	for {
		item, err := d.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if item.Chunk.Offset != int64(len(got[item.Layer])) {
			t.Fatalf("layer %s offset=%d want %d", item.Layer, item.Chunk.Offset, len(got[item.Layer]))
		}
		got[item.Layer] = append(got[item.Layer], item.Chunk.Data...)
	}
	if !bytes.Equal(got["low"], low) || !bytes.Equal(got["high"], high) {
		t.Fatalf("round trip mismatch: low=%d high=%d", len(got["low"]), len(got["high"]))
	}
}

func TestMuxRecordLargerThanReaderChunk(t *testing.T) {
	// A record much larger than the reader's block must still be reassembled,
	// and its payload must arrive intact across block boundaries.
	payload := bytes.Repeat([]byte{0x5A}, 200)
	feed := &trivialFeed{items: []Item{{Layer: "a", Chunk: Chunk{Data: payload}}}}
	mux, err := NewMuxSource(feed, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(mux, Options{ChunkBytes: 9, Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d, _ := NewDemux(r, []string{"a"})
	item, err := d.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(item.Chunk.Data, payload) {
		t.Fatalf("payload corrupted: %d bytes", len(item.Chunk.Data))
	}
}

func TestDemuxEmptyStream(t *testing.T) {
	mux, _ := NewMuxSource(&trivialFeed{}, []string{"a"})
	r, _ := Open(mux, Options{ChunkBytes: 4, Queue: 1})
	defer r.Close()
	d, _ := NewDemux(r, []string{"a"})
	if _, err := d.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream err=%v", err)
	}
}

// rawReader yields an arbitrary byte stream directly to the demux, letting the
// test inject framing the mux would never produce.
func rawReader(t *testing.T, data []byte) *Reader {
	t.Helper()
	r, err := Open(bytes.NewReader(data), Options{ChunkBytes: 3, Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDemuxRejectsTruncatedRecord(t *testing.T) {
	// Header claims 10 payload bytes; only 3 follow.
	data := []byte{0, 0, 0, 0, 10, 1, 2, 3}
	r := rawReader(t, data)
	defer r.Close()
	d, _ := NewDemux(r, []string{"a"})
	if _, err := d.Next(context.Background()); err == nil {
		t.Fatal("truncated record accepted")
	}
}

func TestDemuxRejectsUnknownID(t *testing.T) {
	data := []byte{7, 0, 0, 0, 1, 0xFF}
	r := rawReader(t, data)
	defer r.Close()
	d, _ := NewDemux(r, []string{"a"})
	if _, err := d.Next(context.Background()); err == nil {
		t.Fatal("unknown layer id accepted")
	}
}

func TestMuxValidation(t *testing.T) {
	if _, err := NewMuxSource(nil, []string{"a"}); err == nil {
		t.Fatal("nil feed accepted")
	}
	if _, err := NewMuxSource(&trivialFeed{}, nil); err == nil {
		t.Fatal("no layers accepted")
	}
	if _, err := NewMuxSource(&trivialFeed{}, []string{"a", "a"}); err == nil {
		t.Fatal("duplicate layer accepted")
	}
	if _, err := NewMuxSource(&trivialFeed{}, []string{""}); err == nil {
		t.Fatal("empty layer accepted")
	}
	if _, err := NewDemux(nil, []string{"a"}); err == nil {
		t.Fatal("nil reader accepted")
	}
	r, _ := Open(bytes.NewReader(nil), Options{ChunkBytes: 1, Queue: 1})
	defer r.Close()
	if _, err := NewDemux(r, []string{"a", "a"}); err == nil {
		t.Fatal("duplicate demux layer accepted")
	}
}

func TestMuxUnknownLayerFails(t *testing.T) {
	feed := &trivialFeed{items: []Item{{Layer: "b", Chunk: Chunk{Data: []byte{1}}}}}
	mux, err := NewMuxSource(feed, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := Open(mux, Options{ChunkBytes: 4, Queue: 1})
	defer r.Close()
	d, _ := NewDemux(r, []string{"a"})
	if _, err := d.Next(context.Background()); err == nil {
		t.Fatal("unknown layer accepted")
	}
}
