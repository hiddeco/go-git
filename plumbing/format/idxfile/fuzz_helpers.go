package idxfile

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"

	"github.com/go-git/go-git/v6/plumbing"
)

// buildMinimalIdx constructs a minimal valid idx v2 file with the given
// number of objects and hash size. Used by fuzz seed corpus generation.
func buildMinimalIdx(count, hashSize int) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xff, 't', 'O', 'c'})
	_ = binary.Write(&buf, binary.BigEndian, uint32(2))

	for range 256 {
		_ = binary.Write(&buf, binary.BigEndian, uint32(count))
	}

	for i := range count {
		h := make([]byte, hashSize)

		// Ensure all hashes start with 0x00 (match fanout bucket 0).
		h[1] = byte(i >> 8)
		h[2] = byte(i)
		buf.Write(h)
	}

	// CRC32: count * 4 bytes (all zeros).
	buf.Write(make([]byte, count*4))

	// Offset32: count * 4 bytes (sequential small offsets).
	for i := range count {
		_ = binary.Write(&buf, binary.BigEndian, uint32(i*100))
	}

	// No offset64 entries.

	packChecksum := make([]byte, hashSize)
	packChecksum[0] = 0xAA // recognizable
	buf.Write(packChecksum)
	buf.Write(make([]byte, hashSize)) // idx checksum

	return buf.Bytes()
}

// buildMinimalRev constructs a minimal valid .rev file for the given
// number of objects and hash size. Used by fuzz seed corpus generation.
func buildMinimalRev(count, hashSize int) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{'R', 'I', 'D', 'X'})
	_ = binary.Write(&buf, binary.BigEndian, uint32(1)) // version
	hashID := uint32(1)                                 // sha1
	if hashSize == 32 {
		hashID = 2 // sha256
	}
	_ = binary.Write(&buf, binary.BigEndian, hashID)
	// Entries: identity mapping (already sorted by offset).
	for i := range count {
		_ = binary.Write(&buf, binary.BigEndian, uint32(i))
	}

	buf.Write(make([]byte, hashSize*2))
	return buf.Bytes()
}

// buildOOBOffset64Idx constructs a structurally valid v2 idx file
// whose single 32-bit offset entry is marked as a 64-bit overflow
// (MSB set) but whose lower 31 bits point past the only allocated
// 64-bit offset slot. The idx decodes successfully; using it must
// fail with [ErrMalformedIdxFile] rather than crash.
//
// The returned hash is the name of the single object — pass it to
// FindOffset to exercise the malformed-input path.
//
// Lives outside `_test.go` so the OSS-Fuzz harness, which does not
// see other test files when extracting fuzz targets, can reach it
// from FuzzMemoryIndex's seed corpus.
func buildOOBOffset64Idx() ([]byte, plumbing.Hash) {
	const hashSize = 20

	var buf bytes.Buffer
	buf.Write(idxHeader)
	_ = binary.Write(&buf, binary.BigEndian, uint32(2))

	// Fanout: one object whose first byte is 0x00, so all 256 entries
	// hold the cumulative count 1.
	for range 256 {
		_ = binary.Write(&buf, binary.BigEndian, uint32(1))
	}

	// One name (any valid 20-byte hash with first byte 0x00).
	name := make([]byte, hashSize)
	name[hashSize-1] = 0x01
	buf.Write(name)

	// CRC32 (one entry, value irrelevant).
	buf.Write(make([]byte, 4))

	// Offset32: MSB set, lower 31 bits = 5 → references Offset64[40:48].
	_ = binary.Write(&buf, binary.BigEndian, uint32(0x80000005))

	// Offset64: a single 8-byte slot — the lookup above is out of range.
	_ = binary.Write(&buf, binary.BigEndian, uint64(0x12345678))

	// Pack checksum (zeros — the LazyIndex test passes the same value
	// in as the expected pack hash so it matches).
	buf.Write(make([]byte, hashSize))

	// Idx checksum: SHA1 of everything written so far.
	sum := sha1.Sum(buf.Bytes())
	buf.Write(sum[:])

	var h plumbing.Hash
	h.ResetBySize(hashSize)
	_, _ = h.Write(name)
	return buf.Bytes(), h
}

// nopCloserReaderAt wraps a bytes.Reader to satisfy ReadAtCloser.
type nopCloserReaderAt struct {
	*bytes.Reader
}

func (nopCloserReaderAt) Close() error { return nil }

// fuzzMaxIdxLen caps the byte length of a single idx fuzz input.
// libFuzzer's RSS budget on OSS-Fuzz is ~2.5 GiB, and `MemoryIndex`
// loads the entire idx into memory, so unbounded input length plus
// allocations sized from the declared object count would push the
// process over its limit across many iterations. 64 KiB is enough
// to cover all reasonable structurally-valid prefixes without
// admitting pathological growth.
const fuzzMaxIdxLen = 1 << 16

// idxV2DeclaredSizeIsPlausible reports whether data's declared object
// count (idx v2 fanout[255]) is consistent with len(data). Mirrors
// the size validation in canonical Git's load_idx[1]: reject inputs
// whose declared `nr` would require more bytes than the input
// actually contains, since the eager `MemoryIndex.Decode` allocates
// buckets-sized buffers before the trailing ReadFull aborts.
//
// Returns true for inputs too short to inspect — the decoder rejects
// those naturally on its own.
//
// [1]: https://github.com/git/git/blob/v2.54.0/packfile.c#L246-L251
func idxV2DeclaredSizeIsPlausible(data []byte, hashSize int) bool {
	const (
		magicLen   = 4
		versionLen = 4
		fanoutLen  = 256 * 4
	)
	headerLen := magicLen + versionLen + fanoutLen
	if len(data) < headerLen {
		return true
	}

	nr := binary.BigEndian.Uint32(data[headerLen-4 : headerLen])
	// Minimum on-the-wire size: header + nr * (hash + crc32 + offset32) +
	// pack-checksum + idx-checksum.
	minSize := uint64(headerLen) +
		uint64(nr)*uint64(hashSize+4+4) +
		uint64(2*hashSize)
	return uint64(len(data)) >= minSize
}
