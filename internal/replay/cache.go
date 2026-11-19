// Package replay records consumed license serials so a license cannot be
// presented twice.
//
// Registration is the only mutation and it is atomic: of any number of
// concurrent requests presenting the same serial, exactly one succeeds and the
// rest receive ErrReplay. The cache is in-memory and process-scoped, matching
// the offline, single-node use case.
package replay

import (
	"errors"
	"fmt"
	"sync"
)

// Errors returned by the cache.
var (
	ErrReplay  = errors.New("replay: license serial already used")
	ErrEmptyID = errors.New("replay: empty serial")
)

// Entry describes a consumed serial.
type Entry struct {
	Serial  string
	AssetID string
	// FirstUse is informational; the cache does not interpret time, so tests
	// can supply a deterministic value.
	FirstUseMillis int64
}

// Cache is a concurrency-safe set of consumed serials.
type Cache struct {
	mu      sync.Mutex
	entries map[string]Entry
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{entries: make(map[string]Entry)}
}

// Register atomically records serial. It returns ErrReplay if the serial was
// already recorded, including under concurrent registration. The very first
// successful registration wins; every other caller observes ErrReplay.
func (c *Cache) Register(serial, assetID string, useMillis int64) error {
	if serial == "" {
		return ErrEmptyID
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[serial]; ok {
		return fmt.Errorf("%w: %s first used by asset %s", ErrReplay, serial, existing.AssetID)
	}
	c.entries[serial] = Entry{Serial: serial, AssetID: assetID, FirstUseMillis: useMillis}
	return nil
}

// Contains reports whether serial has been consumed. It is a non-mutating
// diagnostic lookup and is not a substitute for Register.
func (c *Cache) Contains(serial string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[serial]
	return ok
}

// Len returns the number of consumed serials.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Lookup returns the entry for serial without registering it.
func (c *Cache) Lookup(serial string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[serial]
	return e, ok
}
