package recovery

import (
	"testing"

	"example.com/mediaforge/internal/merrors"
)

func payload(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// buildGroup stores n media packets and their parity for sequences starting
// at start, returning the cache and the original payloads.
func buildGroup(t *testing.T, start uint16, n int) (*Cache, [][]byte) {
	t.Helper()
	c, err := NewCache(7, Config{GroupSize: n, MaxGroups: 16, MaxGroupBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	var payloads [][]byte
	for i := 0; i < n; i++ {
		p := payload(byte(i+1), 8)
		payloads = append(payloads, p)
		if err := c.AddMedia(7, start+uint16(i), p); err != nil {
			t.Fatal(err)
		}
	}
	par, err := MakeParity(payloads)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddParity(7, start, par); err != nil {
		t.Fatal(err)
	}
	return c, payloads
}

func expectedRange(start uint16, n int) []uint16 {
	out := make([]uint16, n)
	for i := 0; i < n; i++ {
		out[i] = start + uint16(i)
	}
	return out
}

func TestRepairSingleLoss(t *testing.T) {
	c, payloads := buildGroup(t, 100, 4)
	// Sequence 101 is lost: remove it so the cache sees it missing.
	delete(c.groups[groupKey{start: 100, ssrc: 7}].payloads, 101)

	res, err := c.Repair(101, expectedRange(100, 4))
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(res.Recovered) != 1 || res.Recovered[0] != 101 {
		t.Fatalf("recovered = %v", res.Recovered)
	}
	got, ok := c.Rebuilt(101)
	if !ok {
		t.Fatal("rebuilt payload not reported")
	}
	want := payloads[1]
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recovered payload mismatch at %d: %d vs %d", i, got[i], want[i])
		}
	}
	if c.Stats().Recovered != 1 {
		t.Fatalf("stats = %+v", c.Stats())
	}
}

func TestRepairSingleLossAcrossGroupBoundary(t *testing.T) {
	// Different group sizes and offsets, including sequence wrap.
	c, err := NewCache(3, Config{GroupSize: 4, MaxGroups: 8, MaxGroupBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	start := uint16(65532)
	var payloads [][]byte
	for i := 0; i < 8; i++ {
		p := payload(byte(i+10), 16)
		payloads = append(payloads, p)
		if err := c.AddMedia(3, start+uint16(i), p); err != nil {
			t.Fatal(err)
		}
	}
	// The first group covers 65532..65535, i.e. up to and past the 16-bit
	// sequence wrap.
	par, _ := MakeParity(payloads[:4])
	if err := c.AddParity(3, 65532, par); err != nil {
		t.Fatal(err)
	}
	delete(c.groups[groupKey{start: 65532, ssrc: 3}].payloads, 65534)
	res, err := c.Repair(65534, []uint16{65532, 65533, 65534, 65535})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(res.Recovered) != 1 || res.Recovered[0] != 65534 {
		t.Fatalf("recovered = %v", res.Recovered)
	}
}

func TestRepairMultipleLossUnrecoverable(t *testing.T) {
	c, _ := buildGroup(t, 200, 4)
	g := c.groups[groupKey{start: 200, ssrc: 7}]
	delete(g.payloads, 201)
	delete(g.payloads, 203)

	res, err := c.Repair(201, expectedRange(200, 4))
	if err == nil {
		t.Fatal("expected unrecoverable error")
	}
	if !merrors.IsCategory(err, merrors.Unrecoverable) {
		t.Fatalf("category = %v (%v)", err, err)
	}
	if len(res.Unrecoverable) != 1 {
		t.Fatalf("result = %+v", res)
	}
	gap := res.Unrecoverable[0]
	if gap.From != 201 || gap.Count != 2 || gap.Reason != merrors.Unrecoverable {
		t.Fatalf("gap = %+v", gap)
	}
	if c.Stats().Unrecoverable != 1 {
		t.Fatalf("stats = %+v", c.Stats())
	}
	// The failed group was retired so the cache stays bounded.
	if _, ok := c.groups[groupKey{start: 200, ssrc: 7}]; ok {
		t.Fatal("unrecoverable group was not retired")
	}
}

func TestRepairNoParity(t *testing.T) {
	c, err := NewCache(7, Config{GroupSize: 4, MaxGroups: 8, MaxGroupBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddMedia(7, 300, payload(1, 4)); err != nil {
		t.Fatal(err)
	}
	_, err = c.Repair(300, []uint16{300, 301, 302, 303})
	if !merrors.IsCategory(err, merrors.Unrecoverable) {
		t.Fatalf("err = %v", err)
	}
}

func TestParityChecksumFailure(t *testing.T) {
	c, err := NewCache(7, Config{GroupSize: 4, MaxGroups: 8, MaxGroupBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddMedia(7, 400, payload(1, 8)); err != nil {
		t.Fatal(err)
	}
	err = c.AddParity(7, 400, payload(0, 5))
	if !merrors.IsCategory(err, merrors.Checksum) {
		t.Fatalf("err = %v", err)
	}
	if c.Stats().ChecksumFail != 1 {
		t.Fatalf("stats = %+v", c.Stats())
	}
	// Later data in a fresh group remains decodable.
	if err := c.AddMedia(7, 404, payload(2, 8)); err != nil {
		t.Fatal(err)
	}
	if err := c.AddParity(7, 404, payload(0, 8)); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryBounds(t *testing.T) {
	if _, err := NewCache(1, Config{GroupSize: 0, MaxGroups: 8, MaxGroupBytes: 4096}); err == nil {
		t.Fatal("expected group size error")
	}
	if _, err := NewCache(1, Config{GroupSize: 4, MaxGroups: 0, MaxGroupBytes: 4096}); err == nil {
		t.Fatal("expected max groups error")
	}
	if _, err := NewCache(1, Config{GroupSize: 4, MaxGroups: 8, MaxGroupBytes: 0}); err == nil {
		t.Fatal("expected max bytes error")
	}

	c, _ := NewCache(7, Config{GroupSize: 4, MaxGroups: 2, MaxGroupBytes: 32})
	if err := c.AddMedia(7, 1, payload(1, 16)); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMedia(7, 2, payload(2, 16)); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMedia(7, 3, payload(3, 16)); !merrors.IsCategory(err, merrors.Bounds) {
		t.Fatalf("group bytes err = %v", err)
	}
	if err := c.AddMedia(8, 1, payload(1, 1)); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("ssrc err = %v", err)
	}
}

func TestCacheEvictionBounded(t *testing.T) {
	c, _ := NewCache(7, Config{GroupSize: 1, MaxGroups: 3, MaxGroupBytes: 4096})
	for seq := uint16(0); seq < 10; seq++ {
		if err := c.AddMedia(7, seq, payload(byte(seq), 4)); err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
	}
	if got := len(c.groups); got != 3 {
		t.Fatalf("groups = %d, want 3", got)
	}
}

func TestMakeParityErrors(t *testing.T) {
	if _, err := MakeParity(nil); !merrors.IsCategory(err, merrors.Empty) {
		t.Fatalf("empty err = %v", err)
	}
	if _, err := MakeParity([][]byte{{1, 2}, {1}}); !merrors.IsCategory(err, merrors.Malformed) {
		t.Fatalf("length err = %v", err)
	}
	par, err := MakeParity([][]byte{{0xff, 0x0f}, {0xf0, 0xf0}})
	if err != nil {
		t.Fatal(err)
	}
	if par[0] != 0x0f || par[1] != 0xff {
		t.Fatalf("parity = %x", par)
	}
}

func TestDeterministicRecovery(t *testing.T) {
	run := func() []byte {
		c, _ := buildGroup(t, 500, 4)
		delete(c.groups[groupKey{start: 500, ssrc: 7}].payloads, 502)
		if _, err := c.Repair(502, expectedRange(500, 4)); err != nil {
			t.Fatal(err)
		}
		got, ok := c.Rebuilt(502)
		if !ok {
			t.Fatal("not rebuilt")
		}
		return got
	}
	a, b := run(), run()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic at %d", i)
		}
	}
}

func TestAddParityDuplicateAndNoGroupRepair(t *testing.T) {
	c, _ := buildGroup(t, 600, 2)
	if err := c.AddParity(7, 600, payload(0, 8)); !merrors.IsCategory(err, merrors.Duplicate) {
		t.Fatalf("dup parity err = %v", err)
	}
	_, err := c.Repair(700, []uint16{700})
	if !merrors.IsCategory(err, merrors.Empty) {
		t.Fatalf("no group err = %v", err)
	}
	if got := c.GroupStart(601); got != 600 {
		t.Fatalf("group start = %d", got)
	}
}
