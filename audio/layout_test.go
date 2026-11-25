package audio

import "testing"

func TestLayoutForChannels(t *testing.T) {
	cases := map[int]string{1: "mono", 2: "stereo", 6: "5.1", 8: "7.1", 4: ""}
	for n, want := range cases {
		if got := layoutForChannels(n); got != want {
			t.Fatalf("layoutForChannels(%d)=%q want %q", n, got, want)
		}
	}
}

func TestLayoutOrder(t *testing.T) {
	l, err := LookupLayout("5.1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"L", "R", "C", "LFE", "Ls", "Rs"}
	for i, c := range l.Channels {
		if c.Name != want[i] {
			t.Fatalf("channel %d = %q want %q", i, c.Name, want[i])
		}
	}
}
