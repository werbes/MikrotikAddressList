package main

import (
	"fmt"
	"math/bits"
)

func cidrsForRange(item ipRange, emit func(string) error) error {
	if item.End < item.Start {
		return nil
	}

	start := item.Start
	for {
		alignmentBits := bits.TrailingZeros32(start)
		remaining := uint64(item.End) - uint64(start) + 1
		rangeBits := bits.Len64(remaining) - 1
		blockBits := min(alignmentBits, rangeBits)
		prefix := 32 - blockBits

		if err := emit(fmt.Sprintf("%s/%d", formatIPv4(start), prefix)); err != nil {
			return err
		}

		blockSize := uint64(1) << blockBits
		if blockSize > uint64(item.End)-uint64(start)+1 {
			break
		}
		if blockSize > uint64(item.End)-uint64(start) {
			break
		}
		// #nosec G115 -- blockSize is bounded above by the remaining uint32 range.
		start += uint32(blockSize)
	}
	return nil
}

func formatIPv4(value uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", value>>24, (value>>16)&0xff, (value>>8)&0xff, value&0xff)
}
