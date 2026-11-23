package adapter

// sampleMagic prefixes every valid generated sample; a mismatch marks the
// sample corrupt while truncation is still detected via container indexes.
const sampleMagic uint32 = 0x4D454449 // "MEDI"
