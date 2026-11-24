package main

import (
	"os"

	"mediad/internal/sample"
)

func main() {
	out := os.Args[1]
	for _, kind := range sample.AllKinds() {
		data, _ := sample.Generate(kind)
		_ = os.WriteFile(out+"/"+string(kind)+".ts", data, 0o644)
	}
	trunc, _ := sample.Truncate()
	_ = os.WriteFile(out+"/truncated.ts", trunc, 0o644)
}
