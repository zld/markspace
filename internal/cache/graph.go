// Package cache provides content-addressed derivative storage and a small
// dependency graph for source-scoped invalidation.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"mediacheck/internal/timebase"
)

// Node is one cacheable derivative. Key includes the source digest and the
// complete deterministic configuration.
type Node struct {
	ID        string   `json:"id"`
	TrackID   string   `json:"track_id"`
	SourceSHA string   `json:"source_sha"`
	ConfigSHA string   `json:"config_sha"`
	DependsOn []string `json:"depends_on,omitempty"`
	Key       string   `json:"key"`
}

// DerivationKey hashes source digest, configuration and stable node identity.
func DerivationKey(sourceSHA, configSHA, id string) string {
	h := sha256.New()
	h.Write([]byte("mediacheck-derivative-v1\n"))
	h.Write([]byte(id))
	h.Write([]byte{0})
	h.Write([]byte(sourceSHA))
	h.Write([]byte{0})
	h.Write([]byte(configSHA))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])
}

// ConfigDigest returns a stable hash of JSON-compatible configuration.
func ConfigDigest(cfg any) (string, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", timebase.NewError(timebase.CodeCache, "encode config: %v", err).Wrap(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Graph stores derivative nodes and reverse dependency edges.
type Graph struct {
	nodes map[string]Node
	rev   map[string]map[string]struct{}
}

func NewGraph() *Graph {
	return &Graph{nodes: make(map[string]Node), rev: make(map[string]map[string]struct{})}
}

func (g *Graph) Add(n Node) (Node, error) {
	if n.ID == "" || n.SourceSHA == "" || n.ConfigSHA == "" {
		return Node{}, timebase.NewError(timebase.CodeCache, "cache node requires id, source and config digests")
	}
	n.Key = DerivationKey(n.SourceSHA, n.ConfigSHA, n.ID)
	g.nodes[n.ID] = n
	for _, dep := range n.DependsOn {
		if g.rev[dep] == nil {
			g.rev[dep] = make(map[string]struct{})
		}
		g.rev[dep][n.ID] = struct{}{}
	}
	return n, nil
}

func (g *Graph) Node(id string) (Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// Affected returns transitive derivative ids depending on changed rootID.
// The changed source id itself is excluded; only invalidated work is returned.
func (g *Graph) Affected(rootID string) []string {
	seen := map[string]struct{}{}
	queue := []string{rootID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for child := range g.rev[id] {
			if _, ok := seen[child]; ok {
				continue
			}
			seen[child] = struct{}{}
			queue = append(queue, child)
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Store is an on-disk content-addressed cache.
type Store struct {
	Root string
}

func NewStore(root string) (*Store, error) {
	if root == "" {
		return nil, timebase.NewError(timebase.CodeCache, "empty cache root")
	}
	if err := os.MkdirAll(filepath.Join(root, "objects"), 0o755); err != nil {
		return nil, timebase.NewError(timebase.CodeCache, "create cache root").Wrap(err)
	}
	return &Store{Root: root}, nil
}

func (s *Store) Path(key string) string {
	return filepath.Join(s.Root, "objects", key[:2], key[2:]+".bin")
}

// Get returns cached bytes when the key exists.
func (s *Store) Get(key string) ([]byte, bool, error) {
	b, err := os.ReadFile(s.Path(key))
	if err == nil {
		return b, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, timebase.NewError(timebase.CodeCache, "read cache object").Wrap(err)
}

// Put atomically publishes bytes under key.
func (s *Store) Put(key string, data []byte) (string, error) {
	path := s.Path(key)
	if err := AtomicWriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// AtomicWriteFile writes a same-directory temp file and renames it.
func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return timebase.NewError(timebase.CodeCache, "create derivative directory").Wrap(err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-derivative-*")
	if err != nil {
		return timebase.NewError(timebase.CodeCache, "create temporary derivative").Wrap(err)
	}
	name := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return timebase.NewError(timebase.CodeCache, "write derivative").Wrap(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return timebase.NewError(timebase.CodeCache, "chmod derivative").Wrap(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return timebase.NewError(timebase.CodeCache, "sync derivative").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return timebase.NewError(timebase.CodeCache, "close derivative").Wrap(err)
	}
	if err := os.Rename(name, path); err != nil {
		return timebase.NewError(timebase.CodeCache, "rename derivative").Wrap(err)
	}
	cleanup = false
	return nil
}
