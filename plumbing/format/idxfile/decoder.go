package idxfile

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v6/plumbing/hash"
	"github.com/go-git/go-git/v6/utils/binary"
)

var (
	// ErrUnsupportedVersion is returned by Decode when the idx file version
	// is not supported.
	ErrUnsupportedVersion = errors.New("unsupported version")
	// ErrMalformedIdxFile is returned by Decode when the idx file is corrupted.
	ErrMalformedIdxFile = errors.New("malformed idx file")
)

const (
	fanout = 256

	// maxNamesBytes caps the upfront names-buffer allocation
	// (Fanout[255] * idSize) the decoder will accept from an idx v2 file
	// before any further input validation. The decoder allocates
	// per-object name, offset, and crc buffers from Fanout[255] eagerly,
	// so without a cap a malformed Fanout[255] = 0xFFFFFFFF would request
	// ~80 GiB of name buffer at SHA-1 alone.
	//
	// 512 MiB sits above realistic single-pack object counts — the Linux
	// kernel monorepo packs ~1 Mi objects — while keeping the upfront
	// memory commitment from a single untrusted input bounded. The budget
	// admits ~26.8 Mi entries at SHA-1 and ~16.7 Mi entries at SHA-256.
	// The cap is hash-agnostic by design: as SHA-256 adoption grows it
	// admits roughly half as many entries, but the memory commitment
	// (the thing the threat model cares about) stays fixed.
	maxNamesBytes = 512 << 20
)

// Decoder reads and decodes idx files from an input stream.
type Decoder struct {
	io.Reader
	h hash.Hash
}

// NewDecoder builds a new idx stream decoder, that reads from r.
func NewDecoder(r io.Reader, h hash.Hash) *Decoder {
	tr := io.TeeReader(r, h)
	return &Decoder{tr, h}
}

// Decode reads from the stream and decode the content into the MemoryIndex struct.
func (d *Decoder) Decode(idx *MemoryIndex) error {
	d.h.Reset()
	if err := validateHeader(d); err != nil {
		return err
	}

	flow := []func(*MemoryIndex, io.Reader) error{
		readVersion,
		readFanout,
		readObjectNames,
		readCRC32,
		readOffsets,
		readPackChecksum,
	}

	for _, f := range flow {
		if err := f(idx, d); err != nil {
			return err
		}
	}

	actual := d.h.Sum(nil)
	if err := readIdxChecksum(idx, d); err != nil {
		return err
	}

	if idx.IdxChecksum.Compare(actual) != 0 {
		return fmt.Errorf("%w: checksum mismatch: %q instead of %q",
			ErrMalformedIdxFile, idx.IdxChecksum.String(), hex.EncodeToString(actual))
	}

	return nil
}

func validateHeader(r io.Reader) error {
	h := make([]byte, 4)
	if _, err := io.ReadFull(r, h); err != nil {
		return err
	}

	if !bytes.Equal(h, idxHeader) {
		return ErrMalformedIdxFile
	}

	return nil
}

func readVersion(idx *MemoryIndex, r io.Reader) error {
	v, err := binary.ReadUint32(r)
	if err != nil {
		return err
	}

	if v != VersionSupported {
		return fmt.Errorf("%w: v%d", ErrUnsupportedVersion, v)
	}

	idx.Version = v
	return nil
}

func readFanout(idx *MemoryIndex, r io.Reader) error {
	for k := range fanout {
		n, err := binary.ReadUint32(r)
		if err != nil {
			return err
		}

		if k > 0 && n < idx.Fanout[k-1] {
			return fmt.Errorf("%w: fanout table is not monotonically non-decreasing at entry %d", ErrMalformedIdxFile, k)
		}
		idx.Fanout[k] = n
		idx.FanoutMapping[k] = noMapping
	}

	// Reject inputs that would commit more upfront memory to the names
	// buffer than the configured budget. The multiplication is in uint64
	// to avoid intermediate overflow at attacker-supplied counts (worst
	// case at a hypothetical 64-byte hash: 0xFFFFFFFF * 64 = 256 GiB,
	// well within uint64).
	if want := uint64(idx.Fanout[fanout-1]) * uint64(idx.idSize()); want > maxNamesBytes {
		return fmt.Errorf("%w: declared object count %d would require %d bytes upfront, exceeding the %d-byte limit",
			ErrMalformedIdxFile, idx.Fanout[fanout-1], want, maxNamesBytes)
	}

	return nil
}

func readObjectNames(idx *MemoryIndex, r io.Reader) error {
	idSize := idx.idSize()

	for k := range fanout {
		var buckets uint32
		if k == 0 {
			buckets = idx.Fanout[k]
		} else {
			buckets = idx.Fanout[k] - idx.Fanout[k-1]
		}

		if buckets == 0 {
			continue
		}

		idx.FanoutMapping[k] = len(idx.Names)

		nameLen := int(buckets) * idSize
		bin := make([]byte, nameLen)
		if _, err := io.ReadFull(r, bin); err != nil {
			return err
		}

		idx.Names = append(idx.Names, bin)
		idx.Offset32 = append(idx.Offset32, make([]byte, int(buckets)*4))
		idx.CRC32 = append(idx.CRC32, make([]byte, int(buckets)*4))
	}

	return nil
}

func readCRC32(idx *MemoryIndex, r io.Reader) error {
	for k := range fanout {
		if pos := idx.FanoutMapping[k]; pos != noMapping {
			if _, err := io.ReadFull(r, idx.CRC32[pos]); err != nil {
				return err
			}
		}
	}

	return nil
}

func readOffsets(idx *MemoryIndex, r io.Reader) error {
	var o64cnt int64
	for k := range fanout {
		if pos := idx.FanoutMapping[k]; pos != noMapping {
			if _, err := io.ReadFull(r, idx.Offset32[pos]); err != nil {
				return err
			}

			for p := 0; p < len(idx.Offset32[pos]); p += 4 {
				if idx.Offset32[pos][p]&(byte(1)<<7) > 0 {
					o64cnt++
				}
			}
		}
	}

	if o64cnt > 0 {
		idx.Offset64 = make([]byte, o64cnt*8)
		if _, err := io.ReadFull(r, idx.Offset64); err != nil {
			return err
		}
	}

	return nil
}

func readPackChecksum(idx *MemoryIndex, r io.Reader) error {
	idx.PackfileChecksum.ResetBySize(idx.idSize())
	if _, err := idx.PackfileChecksum.ReadFrom(r); err != nil {
		return err
	}

	return nil
}

func readIdxChecksum(idx *MemoryIndex, r io.Reader) error {
	idx.IdxChecksum.ResetBySize(idx.idSize())
	if _, err := idx.IdxChecksum.ReadFrom(r); err != nil {
		return err
	}

	return nil
}
