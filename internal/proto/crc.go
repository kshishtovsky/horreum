package proto

// crc8Table is the 256-entry lookup table for CRC-8 with polynomial 0x07
// (CRC-8/SMBUS, reflected form, init=0, no xor-out, no reflection).
// Pre-computed at package init so the hot path is a single table lookup
// per byte.
var crc8Table [256]uint8

func init() {
	for i := 0; i < 256; i++ {
		c := uint8(i)
		for j := 0; j < 8; j++ {
			if c&0x80 != 0 {
				c = (c << 1) ^ 0x07
			} else {
				c <<= 1
			}
		}
		crc8Table[i] = c
	}
}

// crc8 computes CRC-8/SMBUS over data. Zero-allocation.
func crc8(data []byte) uint8 {
	c := uint8(0)
	for _, b := range data {
		c = crc8Table[c^b]
	}
	return c
}
