package fixture

// Materialize builds every initialization and media segment through the real
// ISO BMFF marshaller and packetizes the RTP view. It must be called after
// Generate before the parsers consume the program. Segments flagged as
// truncated are physically cut after serialization so that downstream box
// parsers observe a real structural fault, not just an in-memory marker.
func Materialize(p *Program) error {
	all := append(append([]*Representation{}, p.Video...), p.Audio...)
	for _, r := range all {
		initData, err := BuildInit(r)
		if err != nil {
			return err
		}
		r.Init = initData
		for _, seg := range r.Segments {
			data, err := BuildMedia(r, seg)
			if err != nil {
				return err
			}
			seg.Data = data
			if seg.Truncated {
				cut := len(data) * 3 / 5
				seg.Data = data[:cut]
			}
		}
		// Packetize after any physical truncation so RTP loss matches bytes.
		Packetize(r)
	}
	return nil
}
