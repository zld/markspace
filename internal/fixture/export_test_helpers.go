package fixture

// RegressionSeqForTest exposes the fault-injection sequence number to tests
// in other packages. It is a test-only accessor for the unexported constant.
func RegressionSeqForTest() uint32 { return regressionSeq }

// GapSeqForTest exposes the removed-fragment sequence number.
func GapSeqForTest() uint32 { return gapSeq }

// ConflictSeqForTest exposes the duplicate-injection sequence number.
func ConflictSeqForTest() uint32 { return conflictSeq }
