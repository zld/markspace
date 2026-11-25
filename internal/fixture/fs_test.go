package fixture

import (
	"bytes"
	"os"
)

func filesEqual(t1, t2 string) bool {
	a, err := os.ReadFile(t1)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(t2)
	if err != nil {
		return false
	}
	return bytes.Equal(a, b)
}
