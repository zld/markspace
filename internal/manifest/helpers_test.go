package manifest

import "os"

// writeFile is a small test helper kept out of the production API.
func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
