package demux

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	tsTimescale  = 90000
)

const (
	tsStreamH264 = 0x1b
	tsStreamHEVC = 0x24
	tsStreamAAC  = 0x0f
	tsStreamID3  = 0x15
	tsStreamPriv = 0x06
)
