package packhandle

import (
	"errors"
	"io"
	"sync"

	billy "github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
)

// ErrSourceUnconfigured indicates that a Source has been
// deliberately left without an Open/Size implementation. Returned
// by ad-hoc PackHandle constructors when a code path reaches a
// Source whose contract the caller did not intend to honour
// (typically idx/rev on a PackHandle built only for .pack access).
var ErrSourceUnconfigured = errors.New("packhandle: source is unconfigured")

// PackReader is the union of methods Packfile needs against the
// .pack file. Read and Seek share a single cursor protected by an
// internal mutex; do not call Read concurrently with itself or with
// Seek on the same PackReader. ReadAt is intentionally hidden — the
// underlying mmap is concurrent-safe, but callers needing parallel
// random access acquire additional PackReaders via
// PackHandle.OpenPackReader rather than sharing one cursor.
type PackReader interface {
	io.Reader
	io.Seeker
	io.Closer
}

// RandomReader is the union of methods consumers need against the
// .idx and .rev files. ReadAt is concurrent-safe (parallel reads via
// the underlying mmap); Read is provided for idxfile.Decoder's
// sequential consumption and shares a cursor with itself.
type RandomReader interface {
	io.ReaderAt
	io.Reader
	io.Closer
}

// Source provides on-demand access to one of a pack triple's files.
// PackHandle.New takes three Sources (one each for pack, idx, rev);
// each Source's Open is invoked lazily on first need and again after
// each grace-period close. Size is invoked lazily on demand (e.g.
// from cursorReader.Seek(SeekEnd)).
//
// Both Open and Size are required: New panics if either is nil.
type Source struct {
	// Open returns a fresh concurrent-safe random-access read handle.
	// The returned handle's Close is the disposal point for any
	// resources Open holds; SharedFile invokes Close after the grace
	// period or on terminal Close.
	Open func() (billy.ReaderAtCloser, error)
	// Size returns the file's size in bytes. May be called lazily;
	// Source implementations that compute size by opening should
	// cache after the first call to avoid repeated work across
	// grace-period reopens.
	Size func() (int64, error)
}

// Sources bundles the three Sources required to construct a
// PackHandle. The named struct prevents pack/idx/rev argument-order
// swaps at the call site.
type Sources struct {
	Pack, Idx, Rev Source
}

// PathSource returns a Source backed by a billy.Basic + path. This
// is the standard case: util.OpenReaderAt for Open, fs.Stat for
// Size. No caching — Source's SharedFile manages refcounted reuse,
// and Size invocations are rare enough that per-call Stat is fine.
func PathSource(fs billy.Basic, path string) Source {
	return Source{
		Open: func() (billy.ReaderAtCloser, error) {
			return util.OpenReaderAt(fs, path)
		},
		Size: func() (int64, error) {
			info, err := fs.Stat(path)
			if err != nil {
				return 0, err
			}
			return info.Size(), nil
		},
	}
}

// PackMeta is the pack-level metadata PackHandle caches on first
// request. Pack files are immutable post-creation, so this can be
// computed once and reused.
type PackMeta struct {
	ID      plumbing.Hash // footer hash
	Version uint32        // header version
	Count   uint32        // header object count
}

// PackHandle owns the file-descriptor lifecycle for a single pack
// triple. All methods are safe for concurrent use.
type PackHandle struct {
	pack, idx, rev *idxfile.SharedFile

	packSize, idxSize, revSize func() (int64, error)

	closeOnce sync.Once

	meta func(idSize int) (PackMeta, error)
}

// New constructs a PackHandle from three Sources. No I/O occurs at
// construction.
//
// New panics if any of the six Source function fields (Pack.Open,
// Pack.Size, Idx.Open, Idx.Size, Rev.Open, Rev.Size) is nil. Use
// PathSource for the standard case, or construct a Source directly
// with both fields wired.
func New(sources Sources) *PackHandle {
	if sources.Pack.Open == nil {
		panic("packhandle: New: Pack.Open is nil")
	}
	if sources.Pack.Size == nil {
		panic("packhandle: New: Pack.Size is nil")
	}
	if sources.Idx.Open == nil {
		panic("packhandle: New: Idx.Open is nil")
	}
	if sources.Idx.Size == nil {
		panic("packhandle: New: Idx.Size is nil")
	}
	if sources.Rev.Open == nil {
		panic("packhandle: New: Rev.Open is nil")
	}
	if sources.Rev.Size == nil {
		panic("packhandle: New: Rev.Size is nil")
	}
	h := &PackHandle{
		pack: idxfile.NewSharedFile(sources.Pack.Open),
		idx:  idxfile.NewSharedFile(sources.Idx.Open),
		rev:  idxfile.NewSharedFile(sources.Rev.Open),
	}
	// Size getters are NOT cached: Stat is cheap, Seek(SeekEnd) is
	// rare, and caching errors forever poisons the PackHandle on
	// transient I/O failures.
	h.packSize = sources.Pack.Size
	h.idxSize = sources.Idx.Size
	h.revSize = sources.Rev.Size
	h.meta = setupMeta(h)
	return h
}

// setupMeta is a stub until Task 6.
func setupMeta(h *PackHandle) func(idSize int) (PackMeta, error) {
	return func(idSize int) (PackMeta, error) {
		return PackMeta{}, nil
	}
}

func (h *PackHandle) openCursor(sf *idxfile.SharedFile, sizeFn func() (int64, error)) (*cursorReader, error) {
	ra, err := sf.Acquire()
	if err != nil {
		return nil, err
	}
	return newCursorReader(ra, sf.Release, sizeFn), nil
}

// OpenPackReader returns a refcount-holding reader over the .pack
// file. See PackReader for concurrency contract.
func (h *PackHandle) OpenPackReader() (PackReader, error) {
	return h.openCursor(h.pack, h.packSize)
}

// OpenIdxReader returns a refcount-holding reader over the .idx
// file.
func (h *PackHandle) OpenIdxReader() (RandomReader, error) {
	return h.openCursor(h.idx, h.idxSize)
}

// OpenRevReader returns a refcount-holding reader over the .rev
// file.
func (h *PackHandle) OpenRevReader() (RandomReader, error) {
	return h.openCursor(h.rev, h.revSize)
}

// Idx returns the underlying SharedFile for the .idx file, for use
// constructing an idxfile.LazyIndex that shares FDs with this
// PackHandle.
func (h *PackHandle) Idx() *idxfile.SharedFile {
	return h.idx
}

// Rev returns the underlying SharedFile for the .rev file.
func (h *PackHandle) Rev() *idxfile.SharedFile {
	return h.rev
}

// Meta returns the cached pack metadata, computing it on first call
// by transiently Acquiring the pack SharedFile and issuing two
// ReadAts (one for the 12-byte header, one for the idSize-byte
// footer). The result is cached for the PackHandle's lifetime;
// subsequent calls return the same (PackMeta, error) tuple without
// I/O.
//
// Calling Meta with different idSize values across calls is
// undefined: the first call's idSize wins. Mixed-format repos are
// not supported within one storage.
func (h *PackHandle) Meta(idSize int) (PackMeta, error) {
	return h.meta(idSize)
}

// Close marks all three SharedFiles permanently closed. Active
// acquired readers complete normally; FDs release synchronously
// once refcounts reach zero (terminal Close bypasses the grace
// timer). Idempotent: subsequent Close calls return nil.
func (h *PackHandle) Close() error {
	var err error
	h.closeOnce.Do(func() {
		err = errors.Join(
			h.pack.Close(),
			h.idx.Close(),
			h.rev.Close(),
		)
	})
	return err
}
