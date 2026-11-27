package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"mediad/internal/timeline"
)

func readSHA(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func cycleGraph() *timeline.Graph {
	return &timeline.Graph{
		Nodes: []timeline.Node{
			{ID: "a1", Kind: timeline.KindAudio},
			{ID: "a2", Kind: timeline.KindAudio},
			{ID: "m", Kind: timeline.KindManifest},
		},
		Edges: []timeline.Edge{
			{From: "a1", To: "a2"}, {From: "a2", To: "a1"},
			{From: "a1", To: "m"}, {From: "a2", To: "m"},
		},
	}
}
