package main

import "mediasync/internal/verify"

// zeroSummary returns an empty summary used purely for output-shape tests.
func zeroSummary() verify.Summary {
	return verify.Summary{}
}
