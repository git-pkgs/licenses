package aho

const (
	packedUint24Width = 3
	packedByteBits    = 8
	packedTwoByteBits = 16
)

type packedUint24 []byte

func packUint24(values []uint32) packedUint24 {
	packed := make(packedUint24, len(values)*packedUint24Width)
	for index, value := range values {
		if value >= 1<<(packedUint24Width*packedByteBits) {
			panic("aho: value exceeds 24 bits")
		}
		packed.set(uint32(index), value)
	}
	return packed
}

func (p packedUint24) get(index uint32) uint32 {
	offset := int(index) * packedUint24Width
	return uint32(p[offset]) |
		uint32(p[offset+1])<<8 |
		uint32(p[offset+2])<<16
}

func (p packedUint24) set(index, value uint32) {
	offset := int(index) * packedUint24Width
	p[offset] = byte(value)
	p[offset+1] = byte(value >> packedByteBits)
	p[offset+2] = byte(value >> packedTwoByteBits)
}

func (p packedUint24) len() int {
	return len(p) / packedUint24Width
}

func (p packedUint24) dense() []uint32 {
	values := make([]uint32, p.len())
	for index := range values {
		values[index] = p.get(uint32(index))
	}
	return values
}
