package align

// CacheLine is the assumed CPU cache line size for hot counter padding.
const CacheLine = 64

// Pad48 pads two 8-byte atomics to a full cache line.
type Pad48 [CacheLine - 16]byte
