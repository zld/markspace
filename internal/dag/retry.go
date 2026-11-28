package dag

// Do runs fn, retrying on failure up to attempts total tries (attempts <= 1
// means a single try). Idempotency is a property of the caller: the
// scheduler only commits output after fn succeeds, so a retried node never
// appends its output twice.
func Do(attempts int, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
	}
	return err
}
