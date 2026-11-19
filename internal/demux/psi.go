package demux

import (
	"errors"

	"tsinspect/internal/psi"
	"tsinspect/internal/transport"
)

// handlePSI routes a payload to the PAT or a PMT reassembler. Only the PAT PID
// and PMT PIDs named by the effective PAT are trusted up front. A PID outside
// that set is inspected as a PMT candidate only when its payload clearly
// starts a PMT section, which is how an orphan PMT is recognised without ever
// feeding PES bytes to a section assembler.
func (p *Pipeline) handlePSI(pkt *transport.Packet) bool {
	if pkt.PID == patPID {
		return len(p.feedSection(p.pat, pkt, "pat", 0, false)) > 0
	}

	known := p.psiPIDs[pkt.PID]
	if !known && !looksLikePMTSection(pkt) {
		return false
	}
	a, ok := p.pmts[pkt.PID]
	if !ok {
		if len(p.pmts) >= MaxPSIPIDs {
			// Bounded: an attacker spraying section-looking PES cannot grow
			// the assembler table without limit.
			return false
		}
		a = psi.NewAssembler()
		p.pmts[pkt.PID] = a
	}
	return len(p.feedSection(a, pkt, "pmt", pkt.PID, !known)) > 0
}

// feedSection returns the first error produced while consuming this packet.
// A packet that completed a damaged section must not be copied into a slice.
func (p *Pipeline) feedSection(a *psi.Assembler, pkt *transport.Packet, kind string, pid uint16, speculative bool) []error {
	sections, errs := a.Feed(pkt.Payload, pkt.PUSI)
	if speculative {
		// The PID is not part of the effective PAT, so a shape-matching
		// payload may still be arbitrary private data. Its CRC failures are
		// not input damage; only a fully valid PMT is reported as an orphan.
		p.res.IgnoredTables += int64(len(errs))
	} else {
		p.noteSectionErrors(errs)
	}
	for _, sec := range sections {
		want := uint8(psi.TableIDPAT)
		if kind == "pmt" {
			want = psi.TableIDPMT
		}
		if sec.TableID != want {
			// A PMT PID may also carry other tables in some muxes; they are
			// outside this tool's scope, not input damage.
			p.res.IgnoredTables++
			continue
		}
		var err error
		if kind == "pat" {
			err = p.progs.ApplyPAT(sec)
		} else {
			err = p.progs.ApplyPMT(sec, pid)
		}
		if err != nil {
			p.res.TableErrors++
			errs = append(errs, err)
		}
	}
	return errs
}

// looksLikePMTSection is a cheap false-positive filter: PES payloads start
// with 00 00 01, so byte 1 cannot be 0x02, while a PSI packet with
// pointer_field 0 has the table_id immediately after it. The syntax bit and
// section_length are also checked so arbitrary private data rarely matches.
func looksLikePMTSection(pkt *transport.Packet) bool {
	if !pkt.PUSI || len(pkt.Payload) < psi.HeaderSize+1 {
		return false
	}
	p := pkt.Payload
	if p[0] != 0 || p[1] != psi.TableIDPMT || p[2]&0x80 == 0 {
		return false
	}
	length := int(p[2]&0x0f)<<8 | int(p[3])
	return length >= psi.SyntaxSize+psi.CRCSize+1 && length <= psi.MaxSectionLength
}

// noteSectionErrors accounts for reassembly failures. A CRC or length error is
// reported; the offending section is already discarded, so the last effective
// table remains untouched.
func (p *Pipeline) noteSectionErrors(errs []error) {
	for _, err := range errs {
		if errors.Is(err, psi.ErrSectionCRC) {
			p.res.CRCErrors++
			continue
		}
		p.res.SectionErrors++
	}
}

// dropPSIState clears any partial section on a PID whose continuity broke, so
// a damaged section cannot be completed by bytes from a later packet.
func (p *Pipeline) dropPSIState(pid uint16) {
	if pid == patPID {
		p.pat.Reset()
		return
	}
	if a, ok := p.pmts[pid]; ok {
		a.Reset()
	}
}

// refreshPSIPIDs rebuilds the set of PIDs whose payloads are parsed as PSI.
func (p *Pipeline) refreshPSIPIDs() {
	p.psiPIDs = map[uint16]bool{patPID: true}
	for _, pid := range p.progs.PMTPIDs() {
		p.psiPIDs[pid] = true
		if p.pmts[pid] == nil && len(p.pmts) < MaxPSIPIDs {
			p.pmts[pid] = psi.NewAssembler()
		}
	}
}
