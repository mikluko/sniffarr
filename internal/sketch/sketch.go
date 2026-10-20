// Package sketch counts peers in HyperLogLog sketches: a peer is an IP:port
// hashed with a SipHash-2-4 key, and a sketch is bytes in the Aggregate
// Knowledge storage specification v1.0.0, so that sketches from any process
// with the same key merge.
package sketch

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/dchest/siphash"
	hll "github.com/segmentio/go-hll"
)

// settings of every sketch: 1024 registers of 5 bits, with the explicit and
// sparse representations for small sets.
var settings = hll.Settings{Log2m: 10, Regwidth: 5, ExplicitThreshold: hll.AutoExplicitThreshold, SparseEnabled: true}

// KeySize is the size of a Key in bytes.
const KeySize = 16

// Key is the SipHash-2-4 key peers are hashed with.
type Key [KeySize]byte

// NewKey returns a random Key.
func NewKey() (Key, error) {
	var k Key
	_, err := rand.Read(k[:])
	return k, err
}

// ParseKey returns the Key in b, which must be KeySize bytes.
func ParseKey(b []byte) (Key, error) {
	if len(b) != KeySize {
		return Key{}, fmt.Errorf("key of %d bytes, want %d", len(b), KeySize)
	}
	return Key(b), nil
}

// Hash returns the hash of a peer: SipHash-2-4 of its IP, 4 bytes for IPv4
// and 16 for IPv6, followed by its port in big-endian order.
func (k Key) Hash(peer netip.AddrPort) uint64 {
	b := peer.Addr().Unmap().AsSlice()
	b = binary.BigEndian.AppendUint16(b, peer.Port())
	return siphash.Hash(binary.LittleEndian.Uint64(k[:8]), binary.LittleEndian.Uint64(k[8:]), b)
}

// Sketch is a set of hashed peers. The zero value is not usable; use New or
// Parse.
type Sketch struct {
	h hll.Hll
}

// New returns an empty Sketch.
func New() Sketch {
	h, err := hll.NewHll(settings)
	if err != nil {
		panic(err)
	}
	return Sketch{h: h}
}

// Parse returns the Sketch serialized in b; a sketch of other settings is an
// error.
func Parse(b []byte) (Sketch, error) {
	h, err := hll.FromBytes(b)
	if err != nil {
		return Sketch{}, err
	}
	if st := h.Settings(); st.Log2m != settings.Log2m || st.Regwidth != settings.Regwidth {
		return Sketch{}, fmt.Errorf("sketch with log2m %d and regwidth %d, want %d and %d", st.Log2m, st.Regwidth, settings.Log2m, settings.Regwidth)
	}
	return Sketch{h: h}, nil
}

// Add adds a hashed peer.
func (s *Sketch) Add(hash uint64) {
	s.h.AddRaw(hash)
}

// Union adds every peer of o.
func (s *Sketch) Union(o Sketch) error {
	return s.h.StrictUnion(o.h)
}

// Cardinality estimates how many distinct peers were added.
func (s *Sketch) Cardinality() uint64 {
	return s.h.Cardinality()
}

// Bytes serializes the Sketch.
func (s *Sketch) Bytes() []byte {
	return s.h.ToBytes()
}
