package resolve

import "math/bits"

// bitset is a set of version indices of one package (index 0 = newest version).
type bitset []uint64

func newBitset(n int) bitset { return make(bitset, (n+63)/64) }

func (b bitset) has(i int) bool { return b[i/64]&(1<<(i%64)) != 0 }
func (b bitset) set(i int)      { b[i/64] |= 1 << (i % 64) }

func (b bitset) empty() bool {
	for _, w := range b {
		if w != 0 {
			return false
		}
	}
	return true
}

func (b bitset) count() int {
	n := 0
	for _, w := range b {
		n += bits.OnesCount64(w)
	}
	return n
}

// first returns the lowest index in b, or -1.
func (b bitset) first() int {
	for i, w := range b {
		if w != 0 {
			return i*64 + bits.TrailingZeros64(w)
		}
	}
	return -1
}

// intersects reports whether a and b share an index.
func (b bitset) intersects(o bitset) bool {
	for i, w := range b {
		if w&o[i] != 0 {
			return true
		}
	}
	return false
}

func (b bitset) clone() bitset { return append(bitset(nil), b...) }
