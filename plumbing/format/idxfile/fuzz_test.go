package idxfile

import (
	"bytes"
	"crypto"
	"crypto/sha1"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/hash"
)

func FuzzLazyIndex(f *testing.F) {
	idx3 := buildMinimalIdx(3, 20)
	rev3 := buildMinimalRev(3, 20)
	f.Add(idx3, rev3)

	idx0 := buildMinimalIdx(0, 20)
	rev0 := buildMinimalRev(0, 20)
	f.Add(idx0, rev0)

	f.Add([]byte{0xff, 't', 'O', 'c', 0, 0, 0, 2}, []byte{})

	f.Add([]byte{}, []byte{})

	f.Fuzz(func(_ *testing.T, idxData, revData []byte) {
		var packHash plumbing.Hash

		// Try to extract a plausible pack checksum from the idx data.
		// For SHA1 (hashSize=20): packChecksum is at len-40.
		// For SHA256 (hashSize=32): packChecksum is at len-64.
		for _, hs := range []int{20, 32} {
			if len(idxData) >= hs*2 {
				packHash.ResetBySize(hs)
				_, _ = packHash.Write(idxData[len(idxData)-hs*2 : len(idxData)-hs])
			}
		}

		openIdx := func() (ReadAtCloser, error) {
			return nopCloserReaderAt{bytes.NewReader(idxData)}, nil
		}
		var openRev func() (ReadAtCloser, error)
		if len(revData) > 0 {
			openRev = func() (ReadAtCloser, error) {
				return nopCloserReaderAt{bytes.NewReader(revData)}, nil
			}
		}

		idx, err := NewLazyIndex(openIdx, openRev, packHash)
		if err != nil {
			// Expected for most fuzz inputs.
			return
		}
		defer idx.Close()

		// Exercise all Index methods — none should panic.
		testHash := plumbing.NewHash("abcdef1234567890abcdef1234567890abcdef12")
		_, _ = idx.Contains(testHash)
		_, _ = idx.FindOffset(testHash)
		_, _ = idx.FindCRC32(testHash)
		_, _ = idx.FindHash(42)
		_, _ = idx.Count()

		if iter, err := idx.Entries(); err == nil {
			for range 100 {
				if _, err := iter.Next(); err != nil {
					break
				}
			}
			_ = iter.Close()
		}

		if iter, err := idx.EntriesByOffset(); err == nil {
			for range 100 {
				if _, err := iter.Next(); err != nil {
					break
				}
			}
			_ = iter.Close()
		}
	})
}

func FuzzMemoryIndex(f *testing.F) {
	// One seed that decodes successfully but exercises the post-decode
	// methods on a malformed offset64 index — anchors the regression
	// fixed by validating offset64 indices in getOffset.
	oob, _ := buildOOBOffset64Idx()
	f.Add(oob)

	// Structurally valid prefixes; most will fail decode (e.g. the
	// SHA1 trailer is zeros in buildMinimalIdx) but exercise the
	// reader paths.
	f.Add(buildMinimalIdx(3, 20))
	f.Add(buildMinimalIdx(0, 20))
	f.Add([]byte{0xff, 't', 'O', 'c', 0, 0, 0, 2})
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, idxData []byte) {
		// Cap input length and require the declared object count to
		// match the byte size before invoking the eager decoder. Both
		// guards exist because `MemoryIndex.Decode` allocates buffers
		// sized from the fanout total before reading them, so an idx
		// claiming many objects in a small body would push the
		// process over libFuzzer's RSS budget across iterations.
		if len(idxData) > fuzzMaxIdxLen {
			return
		}
		if !idxV2DeclaredSizeIsPlausible(idxData, sha1.Size) {
			return
		}

		idx := new(MemoryIndex)
		d := NewDecoder(bytes.NewReader(idxData), hash.New(crypto.SHA1))
		if err := d.Decode(idx); err != nil {
			// Expected for most fuzz inputs.
			return
		}

		// Exercise all Index methods — none should panic.
		testHash := plumbing.NewHash("abcdef1234567890abcdef1234567890abcdef12")
		_, _ = idx.Contains(testHash)
		_, _ = idx.FindOffset(testHash)
		_, _ = idx.FindCRC32(testHash)
		_, _ = idx.FindHash(42)
		_, _ = idx.Count()

		if iter, err := idx.Entries(); err == nil {
			for range 100 {
				if _, err := iter.Next(); err != nil {
					break
				}
			}
			_ = iter.Close()
		}

		if iter, err := idx.EntriesByOffset(); err == nil {
			for range 100 {
				if _, err := iter.Next(); err != nil {
					break
				}
			}
			_ = iter.Close()
		}
	})
}
