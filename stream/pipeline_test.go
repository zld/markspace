package stream

import (
	"context"
	"testing"
)

func items(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{Seq: uint64(i), Kind: "frame", Data: []byte{byte(i)}}
	}
	return out
}

func TestPipelinePassThrough(t *testing.T) {
	ctx := context.Background()
	p, err := NewPipeline(ctx, Config{BufferSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for _, item := range items(5) {
			if err := p.Send(ctx, item); err != nil {
				return
			}
		}
		p.CloseInput()
	}()
	got, err := Drain(ctx, p)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d items", len(got))
	}
	for i, item := range got {
		if item.Seq != uint64(i) {
			t.Fatalf("item %d has seq %d", i, item.Seq)
		}
	}
}

func TestPipelineTransformsInOrder(t *testing.T) {
	ctx := context.Background()
	double := func(_ context.Context, in <-chan Item, out chan<- Item) error {
		for item := range in {
			item.Data = append(item.Data, item.Data...)
			out <- item
		}
		return nil
	}
	label := func(_ context.Context, in <-chan Item, out chan<- Item) error {
		for item := range in {
			item.Kind = "double"
			out <- item
		}
		return nil
	}
	p, err := NewPipeline(ctx, Config{BufferSize: 3, Name: "transform"}, double, label)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for _, item := range items(4) {
			if err := p.Send(ctx, item); err != nil {
				return
			}
		}
		p.CloseInput()
	}()
	got, err := Drain(ctx, p)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	for i, item := range got {
		if item.Seq != uint64(i) || item.Kind != "double" || len(item.Data) != 2 {
			t.Fatalf("item %d = %+v", i, item)
		}
	}
}
