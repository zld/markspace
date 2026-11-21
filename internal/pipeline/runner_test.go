package pipeline

import (
	"sync"
	"testing"
	"time"

	"mediakey/internal/crypto"
	"mediakey/internal/distributor"
	"mediakey/internal/fixture"
	"mediakey/internal/model"
	"mediakey/internal/replay"
)

func testClock() model.Clock {
	t, _ := time.Parse(time.RFC3339, "2026-11-01T09:00:00Z")
	return model.FixedClock{T: t}
}

func reasons(rep *model.Report) map[string]int {
	m := map[string]int{}
	for _, r := range rep.Results {
		m[r.FailureReason]++
	}
	return m
}

func runMixed(t *testing.T, workers int) (*Runner, *fixture.Sample, *model.Report) {
	t.Helper()
	sample := fixture.BuildSample("pkg-test", 12, 7)
	sample.ApplyScenario("mixed")
	r := New("pkg-test", Config{Clock: testClock(), RotateEvery: 4, Workers: workers})
	rep, err := r.Run(sample)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return r, sample, rep
}

func TestMixedScenarioCountersAndReasons(t *testing.T) {
	_, _, rep := runMixed(t, 4)
	if rep.RotationCount != 2 {
		t.Fatalf("rotation count = %d, want 2", rep.RotationCount)
	}
	if rep.ShardCount != 12 || rep.Accepted != 8 || rep.Rejected != 4 {
		t.Fatalf("counters wrong: %+v", rep)
	}
	got := reasons(rep)
	for _, want := range []string{
		model.ReasonKeyExpired,
		model.ReasonLicenseExpired,
		model.ReasonLicenseReplay,
		model.ReasonAuthTag,
	} {
		if got[want] != 1 {
			t.Fatalf("reason %s count = %d, want 1 (all reasons: %v)", want, got[want], got)
		}
	}
	for _, res := range rep.Results {
		if res.Status == model.StatusAccepted && res.FailureReason != model.ReasonNone {
			t.Fatalf("accepted shard %d carries reason %s", res.ShardID, res.FailureReason)
		}
	}
}

func TestStableOrderingUnderParallelism(t *testing.T) {
	for _, workers := range []int{1, 3, 8, 16} {
		_, _, rep := runMixed(t, workers)
		for i, r := range rep.Results {
			if r.ShardID != i {
				t.Fatalf("workers=%d: row %d has shard %d, want ordered output", workers, i, r.ShardID)
			}
		}
	}
}

func TestRotationAndRetryReuseKeyID(t *testing.T) {
	sample := fixture.BuildSample("pkg-retry", 12, 1)
	sample.ApplyScenario("mixed")
	r := New("pkg-retry", Config{Clock: testClock(), RotateEvery: 4, Workers: 3})
	first, err := r.Run(sample)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Retrying the whole package must reuse cached shard->key assignments.
	second, err := r.Run(sample)
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if r.RotationCount() != 2 {
		t.Fatalf("rotation after retry = %d, want 2", r.RotationCount())
	}
	for i := range first.Results {
		if first.Results[i].KeyID != second.Results[i].KeyID {
			t.Fatalf("shard %d key id changed on retry", i)
		}
	}
}

func TestIndividualFaults(t *testing.T) {
	cases := []struct {
		name  string
		fault string
		want  string
	}{
		{"key expired", fixture.FaultKeyExpired, model.ReasonKeyExpired},
		{"license expired", fixture.FaultLicenseExpired, model.ReasonLicenseExpired},
		{"replay", fixture.FaultLicenseReplay, model.ReasonLicenseReplay},
		{"auth tag", fixture.FaultAuthTag, model.ReasonAuthTag},
		{"manifest", fixture.FaultManifest, model.ReasonManifest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sample := fixture.BuildSample("pkg-fault", 6, 3)
			sample.SetFault(2, tc.fault)
			r := New("pkg-fault", Config{Clock: testClock(), RotateEvery: 2, Workers: 2})
			rep, err := r.Run(sample)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			res := rep.Results[2]
			if res.Status != model.StatusRejected || res.FailureReason != tc.want {
				t.Fatalf("got status=%s reason=%s, want REJECTED/%s", res.Status, res.FailureReason, tc.want)
			}
			if rep.Accepted != 5 || rep.Rejected != 1 {
				t.Fatalf("single failure must not abort package: %+v", rep)
			}
		})
	}
}

func TestUnknownKeyMapping(t *testing.T) {
	rot := distributor.New(testClock(), "pkg-unknown", 4, time.Hour)
	if _, err := rot.Material("key-never-issued"); err != distributor.ErrUnknownKey {
		t.Fatalf("want ErrUnknownKey, got %v", err)
	}
	if !rot.IsExpired("key-never-issued") {
		t.Fatal("unknown key must report expired/unknown")
	}
	// Pipeline-level mapping: a packet whose key id is not in the rotator.
	r := New("pkg-unknown", Config{Clock: testClock(), RotateEvery: 4, Workers: 1})
	item := &work{
		shard:   model.Shard{ShardID: 0, Size: 4},
		clock:   testClock(),
		license: model.License{},
		packet:  &crypto.Packet{ShardID: 0, KeyID: "key-never-issued", Ciphertext: []byte{1, 2}},
	}
	res := r.process("pkg-unknown", "digest", item)
	if res.FailureReason != model.ReasonUnknownKey {
		t.Fatalf("want UNKNOWN_KEY, got %s", res.FailureReason)
	}
}

func TestReplayLedgerConcurrentSinglePass(t *testing.T) {
	ledger := replay.New()
	const goroutines = 64
	var wg sync.WaitGroup
	pass := make(chan bool, goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			pass <- ledger.Consume("shared-nonce")
		}()
	}
	close(start)
	wg.Wait()
	close(pass)
	allowed := 0
	for ok := range pass {
		if ok {
			allowed++
		}
	}
	if allowed != 1 {
		t.Fatalf("nonce passed %d times concurrently, want exactly 1", allowed)
	}
}

func TestBoundaryShardsAccepted(t *testing.T) {
	sample := fixture.BuildSample("pkg-boundary", 12, 9)
	sizes := map[int]int{}
	for _, sh := range sample.Manifest.Shards {
		sizes[sh.ShardID] = sh.Size
	}
	if sizes[0] != 0 {
		t.Fatalf("first shard must be empty, got %d", sizes[0])
	}
	if sizes[1] != fixture.ShardBlockSize {
		t.Fatalf("second shard must hit the limit, got %d", sizes[1])
	}
	last := 11
	if sizes[last] == 0 || sizes[last] >= fixture.ShardBlockSize {
		t.Fatalf("last shard must be a short block, got %d", sizes[last])
	}
	r := New("pkg-boundary", Config{Clock: testClock(), RotateEvery: 4, Workers: 4})
	rep, err := r.Run(sample)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Rejected != 0 || rep.Accepted != 12 {
		t.Fatalf("clean sample must fully pass: %+v", rep)
	}
}

func TestExpiredClockDoesNotFallBackToNewestKey(t *testing.T) {
	sample := fixture.BuildSample("pkg-clock", 8, 2)
	sample.SetFault(1, fixture.FaultKeyExpired)
	r := New("pkg-clock", Config{Clock: testClock(), RotateEvery: 2, Workers: 2})
	rep, err := r.Run(sample)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Results[1].FailureReason != model.ReasonKeyExpired {
		t.Fatalf("delayed shard got %s, want KEY_EXPIRED (no newer-key retry)", rep.Results[1].FailureReason)
	}
}

func TestTamperedPlaintextReportsManifestMismatch(t *testing.T) {
	dir := t.TempDir()
	sample := fixture.BuildSample("pkg-tamper", 8, 5)
	if err := sample.Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := fixture.Load(dir)
	if err != nil {
		t.Fatalf("load clean bundle: %v", err)
	}
	// Corrupt one stored plaintext byte while leaving the manifest intact.
	target := loaded.Plaintexts[3]
	target[0] ^= 0xFF
	loaded.Plaintexts[3] = target
	r := New("pkg-tamper", Config{Clock: testClock(), RotateEvery: 4, Workers: 2})
	rep, err := r.Run(loaded)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Results[3].FailureReason != model.ReasonManifest {
		t.Fatalf("tampered plaintext got %s, want MANIFEST_MISMATCH", rep.Results[3].FailureReason)
	}
	if rep.Rejected != 1 || rep.Accepted != 7 {
		t.Fatalf("one tampered shard must not abort the package: %+v", rep)
	}
}
