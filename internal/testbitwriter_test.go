package rtpreplay

// testBitWriter is a small MSB-first writer shared by fixture-style tests.
type testBitWriter struct {
	buf  []byte
	bits uint
}

func newTestBitWriter() *testBitWriter { return &testBitWriter{} }

func (w *testBitWriter) write(v uint32, n uint) {
	for i := int(n) - 1; i >= 0; i-- {
		if w.bits%8 == 0 {
			w.buf = append(w.buf, 0)
		}
		if v>>uint(i)&1 == 1 {
			w.buf[len(w.buf)-1] |= 1 << (7 - w.bits%8)
		}
		w.bits++
	}
}

func (w *testBitWriter) bytes() []byte { return w.buf }
