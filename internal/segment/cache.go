package segment

// SampleCache retains raw sample payloads only for the segment currently being
// processed. Evidence rows contain metadata and survive Release.
type SampleCache struct {
	batches       map[string][][]byte
	retainedBytes int
}

// NewSampleCache creates an empty segment-scoped cache.
func NewSampleCache() *SampleCache {
	return &SampleCache{batches: make(map[string][][]byte)}
}

// Retain stores a batch under segmentID, replacing any previous batch.
func (c *SampleCache) Retain(segmentID string, payloads [][]byte) {
	c.Release(segmentID)
	total := 0
	for _, p := range payloads {
		total += len(p)
	}
	c.batches[segmentID] = payloads
	c.retainedBytes += total
}

// Get returns the retained batch, if present.
func (c *SampleCache) Get(segmentID string) [][]byte {
	return c.batches[segmentID]
}

// Release drops all payload references for one segment and returns the number
// of bytes released.
func (c *SampleCache) Release(segmentID string) int {
	payloads, ok := c.batches[segmentID]
	if !ok {
		return 0
	}
	total := 0
	for _, p := range payloads {
		total += len(p)
	}
	c.retainedBytes -= total
	delete(c.batches, segmentID)
	return total
}

// RetainedBytes is the current raw payload memory tracked by the cache.
func (c *SampleCache) RetainedBytes() int {
	return c.retainedBytes
}

// ProcessBatch retains a batch, processes it, and always releases that segment.
func ProcessBatch(c *SampleCache, segmentID string, payloads [][]byte, process func([][]byte) error) error {
	c.Retain(segmentID, payloads)
	err := process(c.Get(segmentID))
	released := c.Release(segmentID)
	if released < 0 {
		panic("segment cache byte accounting became negative")
	}
	return err
}
