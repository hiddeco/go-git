package packhandle

import (
	"bytes"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	billy "github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
)

// instrumentedSource wraps a PathSource with atomic counters for
// opens and size queries, used by timer-independence tests.
func instrumentedSource(fs billy.Basic, path string) (Source, *atomic.Int32, *atomic.Int32) {
	var opens, sizes atomic.Int32
	base := PathSource(fs, path)
	return Source{
		Open: func() (billy.ReaderAtCloser, error) {
			opens.Add(1)
			return base.Open()
		},
		Size: func() (int64, error) {
			sizes.Add(1)
			return base.Size()
		},
	}, &opens, &sizes
}

func writeMemFile(t *testing.T, fs billy.Basic, path string, data []byte) {
	t.Helper()
	require.NoError(t, util.WriteFile(fs, path, data, 0o644))
}

func newMemPackHandle(t *testing.T, packBytes, idxBytes, revBytes []byte) (*PackHandle, billy.Basic) {
	t.Helper()
	fs := memfs.New()
	writeMemFile(t, fs, "pack-deadbeef.pack", packBytes)
	writeMemFile(t, fs, "pack-deadbeef.idx", idxBytes)
	writeMemFile(t, fs, "pack-deadbeef.rev", revBytes)
	ph := New(Sources{
		Pack: PathSource(fs, "pack-deadbeef.pack"),
		Idx:  PathSource(fs, "pack-deadbeef.idx"),
		Rev:  PathSource(fs, "pack-deadbeef.rev"),
	})
	return ph, fs
}

func TestPackHandle_LazyOpen(t *testing.T) {
	t.Parallel()
	ph, _ := newMemPackHandle(t, []byte("pack-data"), []byte("idx-data"), []byte("rev-data"))
	defer ph.Close()

	pr, err := ph.OpenPackReader()
	require.NoError(t, err)
	defer pr.Close()

	buf := make([]byte, 9)
	n, err := pr.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, 9, n)
	assert.Equal(t, "pack-data", string(buf))
}

func TestPackHandle_RefcountedShare(t *testing.T) {
	t.Parallel()
	fs := memfs.New()
	writeMemFile(t, fs, "p.pack", []byte("pack-data"))
	writeMemFile(t, fs, "p.idx", []byte("idx-data"))
	writeMemFile(t, fs, "p.rev", []byte("rev-data"))

	packSrc, opens, _ := instrumentedSource(fs, "p.pack")
	ph := New(Sources{
		Pack: packSrc,
		Idx:  PathSource(fs, "p.idx"),
		Rev:  PathSource(fs, "p.rev"),
	})
	defer ph.Close()

	r1, err := ph.OpenPackReader()
	require.NoError(t, err)
	r2, err := ph.OpenPackReader()
	require.NoError(t, err)

	// Both share one underlying open.
	assert.Equal(t, int32(1), opens.Load())

	// Independent cursors.
	b1 := make([]byte, 4)
	b2 := make([]byte, 4)
	_, err = r1.Read(b1)
	require.NoError(t, err)
	_, err = r2.Read(b2)
	require.NoError(t, err)
	assert.Equal(t, "pack", string(b1))
	assert.Equal(t, "pack", string(b2))

	assert.NoError(t, r1.Close())
	assert.NoError(t, r2.Close())
}

func TestPackHandle_IndependentTimers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		fs := memfs.New()
		writeMemFile(t, fs, "p.pack", []byte("pack-data"))
		writeMemFile(t, fs, "p.idx", []byte("idx-data"))
		writeMemFile(t, fs, "p.rev", []byte("rev-data"))

		packSrc, packOpens, _ := instrumentedSource(fs, "p.pack")
		idxSrc, idxOpens, _ := instrumentedSource(fs, "p.idx")
		ph := New(Sources{
			Pack: packSrc,
			Idx:  idxSrc,
			Rev:  PathSource(fs, "p.rev"),
		})
		defer ph.Close()

		// Open + close pack: pack opener count = 1.
		pr, err := ph.OpenPackReader()
		require.NoError(t, err)
		require.NoError(t, pr.Close())
		assert.Equal(t, int32(1), packOpens.Load())

		// 800ms in synthetic time; pack grace at 1000ms — not yet fired.
		time.Sleep(800 * time.Millisecond)

		// Open + close idx: idx opener count = 1, idx timer starts.
		ir, err := ph.OpenIdxReader()
		require.NoError(t, err)
		require.NoError(t, ir.Close())
		assert.Equal(t, int32(1), idxOpens.Load())

		// 400ms more. Total since pack close: 1200ms (timer fired).
		// Total since idx close: 400ms (timer not fired).
		time.Sleep(400 * time.Millisecond)
		synctest.Wait()

		// Re-open both. Pack should re-invoke opener (timer fired);
		// idx should reuse (timer still pending).
		pr2, err := ph.OpenPackReader()
		require.NoError(t, err)
		defer pr2.Close()
		assert.Equal(t, int32(2), packOpens.Load(), "pack should have re-opened")

		ir2, err := ph.OpenIdxReader()
		require.NoError(t, err)
		defer ir2.Close()
		assert.Equal(t, int32(1), idxOpens.Load(), "idx should still be warm")
	})
}

func TestPackHandle_IteratorHeldReader(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		fs := memfs.New()
		writeMemFile(t, fs, "p.pack", []byte("pack-data"))
		writeMemFile(t, fs, "p.idx", []byte("idx-data"))
		writeMemFile(t, fs, "p.rev", []byte("rev-data"))

		packSrc, packOpens, _ := instrumentedSource(fs, "p.pack")
		ph := New(Sources{
			Pack: packSrc,
			Idx:  PathSource(fs, "p.idx"),
			Rev:  PathSource(fs, "p.rev"),
		})
		defer ph.Close()

		pr, err := ph.OpenPackReader()
		require.NoError(t, err)

		// Past the grace period.
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()

		// Reader still works.
		buf := make([]byte, 4)
		n, err := pr.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, 4, n)
		assert.Equal(t, "pack", string(buf))

		// Opener was called exactly once (refcount kept FD alive).
		assert.Equal(t, int32(1), packOpens.Load())

		require.NoError(t, pr.Close())
	})
}

func TestPackHandle_CloseTerminal(t *testing.T) {
	t.Parallel()
	ph, _ := newMemPackHandle(t, []byte("pack-data"), []byte("idx-data"), []byte("rev-data"))
	require.NoError(t, ph.Close())

	_, err := ph.OpenPackReader()
	assert.ErrorIs(t, err, idxfile.ErrSharedFileClosed)
	_, err = ph.OpenIdxReader()
	assert.ErrorIs(t, err, idxfile.ErrSharedFileClosed)
	_, err = ph.OpenRevReader()
	assert.ErrorIs(t, err, idxfile.ErrSharedFileClosed)
}

func TestPackHandle_CloseIdempotent(t *testing.T) {
	t.Parallel()
	ph, _ := newMemPackHandle(t, []byte("pack-data"), []byte("idx-data"), []byte("rev-data"))
	require.NoError(t, ph.Close())
	require.NoError(t, ph.Close())
	require.NoError(t, ph.Close())
}

func TestPackHandle_InMemoryRevSource(t *testing.T) {
	t.Parallel()
	// Exercise the Source flexibility: a Source backed by an
	// in-memory bytes buffer (simulating dotgit's rev fallback).
	revBytes := []byte("synthetic-rev-content")
	revSrc := Source{
		Open: func() (billy.ReaderAtCloser, error) {
			return &nopReaderAtCloser{bytes.NewReader(revBytes)}, nil
		},
		Size: func() (int64, error) {
			return int64(len(revBytes)), nil
		},
	}

	fs := memfs.New()
	writeMemFile(t, fs, "p.pack", []byte("pack-data"))
	writeMemFile(t, fs, "p.idx", []byte("idx-data"))

	ph := New(Sources{
		Pack: PathSource(fs, "p.pack"),
		Idx:  PathSource(fs, "p.idx"),
		Rev:  revSrc,
	})
	defer ph.Close()

	rr, err := ph.OpenRevReader()
	require.NoError(t, err)
	defer rr.Close()

	buf := make([]byte, len(revBytes))
	n, err := rr.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, len(revBytes), n)
	assert.Equal(t, revBytes, buf)
}

// nopReaderAtCloser is a billy.ReaderAtCloser backed by a
// *bytes.Reader with a no-op Close.
type nopReaderAtCloser struct {
	*bytes.Reader
}

func (n *nopReaderAtCloser) Close() error { return nil }

func TestPackHandle_OpenError(t *testing.T) {
	t.Parallel()
	fs := memfs.New() // empty — files don't exist
	ph := New(Sources{
		Pack: PathSource(fs, "missing.pack"),
		Idx:  PathSource(fs, "missing.idx"),
		Rev:  PathSource(fs, "missing.rev"),
	})
	defer ph.Close()

	_, err := ph.OpenPackReader()
	assert.Error(t, err)
	assert.False(t, errors.Is(err, idxfile.ErrSharedFileClosed))
}

func TestPackHandle_NewPanicsOnNilSource(t *testing.T) {
	t.Parallel()
	good := PathSource(memfs.New(), "missing") // never invoked; only the fields matter

	cases := []struct {
		name    string
		mutate  func(s *Sources)
		message string
	}{
		{
			name:    "Pack.Open nil",
			mutate:  func(s *Sources) { s.Pack.Open = nil },
			message: "Pack.Open",
		},
		{
			name:    "Pack.Size nil",
			mutate:  func(s *Sources) { s.Pack.Size = nil },
			message: "Pack.Size",
		},
		{
			name:    "Idx.Open nil",
			mutate:  func(s *Sources) { s.Idx.Open = nil },
			message: "Idx.Open",
		},
		{
			name:    "Idx.Size nil",
			mutate:  func(s *Sources) { s.Idx.Size = nil },
			message: "Idx.Size",
		},
		{
			name:    "Rev.Open nil",
			mutate:  func(s *Sources) { s.Rev.Open = nil },
			message: "Rev.Open",
		},
		{
			name:    "Rev.Size nil",
			mutate:  func(s *Sources) { s.Rev.Size = nil },
			message: "Rev.Size",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srcs := Sources{Pack: good, Idx: good, Rev: good}
			tc.mutate(&srcs)
			require.PanicsWithValue(t,
				"packhandle: New: "+tc.message+" is nil",
				func() { _ = New(srcs) },
			)
		})
	}

	// Zero-value Sources panics on the first missing field
	// (Pack.Open). Guards against future field-reorder regressions.
	require.Panics(t, func() { _ = New(Sources{}) })
}

func TestPackHandle_SizeErrorNotCached(t *testing.T) {
	t.Parallel()
	fs := memfs.New()
	writeMemFile(t, fs, "p.pack", []byte("pack-data"))
	writeMemFile(t, fs, "p.idx", []byte("idx-data"))
	writeMemFile(t, fs, "p.rev", []byte("rev-data"))

	base := PathSource(fs, "p.pack")

	// First Size call returns a transient error; subsequent calls
	// delegate to the underlying PathSource and succeed.
	var calls atomic.Int32
	flakySize := Source{
		Open: base.Open,
		Size: func() (int64, error) {
			if calls.Add(1) == 1 {
				return 0, errors.New("transient stat failure")
			}
			return base.Size()
		},
	}

	ph := New(Sources{
		Pack: flakySize,
		Idx:  PathSource(fs, "p.idx"),
		Rev:  PathSource(fs, "p.rev"),
	})
	defer ph.Close()

	pr, err := ph.OpenPackReader()
	require.NoError(t, err)
	defer pr.Close()

	// First Seek(SeekEnd) propagates the transient error.
	_, err = pr.Seek(0, io.SeekEnd)
	require.Error(t, err)

	// Subsequent Seek(SeekEnd) must re-probe and succeed: the
	// transient error must NOT have been cached.
	end, err := pr.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	assert.Equal(t, int64(len("pack-data")), end)

	// Sanity: Size was invoked twice (once flaky, once successful).
	assert.GreaterOrEqual(t, calls.Load(), int32(2))
}
