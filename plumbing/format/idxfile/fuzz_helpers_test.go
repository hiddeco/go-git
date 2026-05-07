package idxfile

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIdxV2DeclaredSizeIsPlausible(t *testing.T) {
	t.Parallel()

	const hashSize = 20

	t.Run("too short to inspect is allowed", func(t *testing.T) {
		t.Parallel()
		assert.True(t, idxV2DeclaredSizeIsPlausible(nil, hashSize))
		assert.True(t, idxV2DeclaredSizeIsPlausible(make([]byte, 100), hashSize))
	})

	t.Run("declared count plausible for input length", func(t *testing.T) {
		t.Parallel()
		// A real minimal 3-object idx — header + names + crc + offsets
		// + two trailing checksums — passes.
		assert.True(t, idxV2DeclaredSizeIsPlausible(buildMinimalIdx(3, hashSize), hashSize))
		assert.True(t, idxV2DeclaredSizeIsPlausible(buildMinimalIdx(0, hashSize), hashSize))
	})

	t.Run("absurd declared count rejected", func(t *testing.T) {
		t.Parallel()
		// Header + fanout where fanout[255] = 2^32-1 — the hostile shape
		// that would drive Decode into a multi-gigabyte make([]byte, ...).
		hostile := make([]byte, 8+256*4)
		copy(hostile, []byte{0xff, 't', 'O', 'c'})
		binary.BigEndian.PutUint32(hostile[4:], 2)
		binary.BigEndian.PutUint32(hostile[8+255*4:], 0xFFFFFFFF)

		assert.False(t, idxV2DeclaredSizeIsPlausible(hostile, hashSize))
	})

	t.Run("declared count larger than fits is rejected", func(t *testing.T) {
		t.Parallel()
		// Body has the full header+fanout but only enough room for ~10
		// entries; the declared count of 100 would need substantially
		// more, so the helper rejects it.
		body := make([]byte, 8+256*4+10*(hashSize+4+4))
		copy(body, []byte{0xff, 't', 'O', 'c'})
		binary.BigEndian.PutUint32(body[4:], 2)
		binary.BigEndian.PutUint32(body[8+255*4:], 100)

		assert.False(t, idxV2DeclaredSizeIsPlausible(body, hashSize))
	})
}
