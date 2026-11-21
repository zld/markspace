package ts

import "fmt"

func errNoPAT() error { return fmt.Errorf("ts: no PAT observed in segment") }

func errNoPMT(pid uint16) error {
	return fmt.Errorf("ts: no PMT observed on PID %#x", pid)
}
