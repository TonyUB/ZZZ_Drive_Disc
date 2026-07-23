package scan

import "encoding/binary"

const sessionPadSize = 4096

// DeriveSessionPad implements the standard MT19937-64 stream used after an
// active login session has decrypted and verified serverRand. The session seed
// is clientRand XOR serverRand. A passive observer does not know clientRand and
// therefore cannot call this function from captured traffic alone.
func DeriveSessionPad(clientRand, serverRand uint64) []byte {
	generator := newMT19937_64(clientRand ^ serverRand)
	pad := make([]byte, sessionPadSize)
	for offset := 0; offset < len(pad); offset += 8 {
		binary.LittleEndian.PutUint64(pad[offset:offset+8], generator.next())
	}
	return pad
}

func XORBytes(data, pad []byte) {
	if len(pad) == 0 {
		return
	}
	for i := range data {
		data[i] ^= pad[i%len(pad)]
	}
}

// ZeroBytes removes short-lived plaintext secrets from a mutable buffer when a
// Source no longer needs them. Go cannot guarantee removal of immutable string
// copies, so Sources should keep credentials in byte slices where possible.
func ZeroBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}

type mt19937_64 struct {
	state [312]uint64
	index int
}

func newMT19937_64(seed uint64) *mt19937_64 {
	g := &mt19937_64{index: 312}
	g.state[0] = seed
	for i := 1; i < len(g.state); i++ {
		previous := g.state[i-1]
		g.state[i] = 6364136223846793005*(previous^(previous>>62)) + uint64(i)
	}
	return g
}

func (g *mt19937_64) next() uint64 {
	if g.index >= len(g.state) {
		g.twist()
	}
	value := g.state[g.index]
	g.index++
	value ^= (value >> 29) & 0x5555555555555555
	value ^= (value << 17) & 0x71D67FFFEDA60000
	value ^= (value << 37) & 0xFFF7EEE000000000
	value ^= value >> 43
	return value
}

func (g *mt19937_64) twist() {
	const (
		matrixA = uint64(0xB5026F5AA96619E9)
		upper   = uint64(0xFFFFFFFF80000000)
		lower   = uint64(0x000000007FFFFFFF)
	)
	for i := range g.state {
		x := (g.state[i] & upper) | (g.state[(i+1)%len(g.state)] & lower)
		next := g.state[(i+156)%len(g.state)] ^ (x >> 1)
		if x&1 != 0 {
			next ^= matrixA
		}
		g.state[i] = next
	}
	g.index = 0
}
