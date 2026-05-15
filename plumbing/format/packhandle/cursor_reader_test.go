package packhandle

import (
	"bytes"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackingReader is a billy.ReaderAtCloser backed by a bytes.Reader
// that tracks Close + counts ReadAt calls.
type trackingReader struct {
	*bytes.Reader
	readAtCalls atomic.Int32
	closed      atomic.Bool
}

func (t *trackingReader) ReadAt(p []byte, off int64) (int, error) {
	t.readAtCalls.Add(1)
	return t.Reader.ReadAt(p, off)
}

func (t *trackingReader) Close() error { t.closed.Store(true); return nil }

func newCursorReaderForTest(data []byte) (*cursorReader, *trackingReader, *atomic.Int32) {
	tr := &trackingReader{Reader: bytes.NewReader(data)}
	var releases atomic.Int32
	sizeFn := func() (int64, error) { return int64(len(data)), nil }
	cr := newCursorReader(tr, func() { releases.Add(1) }, sizeFn)
	return cr, tr, &releases
}

func TestCursorReader_SequentialRead(t *testing.T) {
	t.Parallel()
	cr, _, releases := newCursorReaderForTest([]byte("hello world"))
	t.Cleanup(func() { _ = cr.Close() })

	buf := make([]byte, 5)
	n, err := cr.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", string(buf))

	n, err = cr.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, " worl", string(buf))

	// Read at the tail: bytes.Reader.ReadAt returns (1, io.EOF) when
	// fewer bytes than requested are available at the offset. We
	// propagate that shape directly (no EOF suppression).
	n, err = cr.Read(buf)
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 1, n)
	assert.Equal(t, "d", string(buf[:n]))

	require.NoError(t, cr.Close())
	assert.Equal(t, int32(1), releases.Load())
}

func TestCursorReader_Seek(t *testing.T) {
	t.Parallel()
	cr, _, _ := newCursorReaderForTest([]byte("0123456789"))
	t.Cleanup(func() { _ = cr.Close() })

	pos, err := cr.Seek(3, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, int64(3), pos)

	buf := make([]byte, 2)
	_, err = cr.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "34", string(buf))

	pos, err = cr.Seek(2, io.SeekCurrent)
	require.NoError(t, err)
	assert.Equal(t, int64(7), pos)

	pos, err = cr.Seek(-3, io.SeekEnd)
	require.NoError(t, err)
	assert.Equal(t, int64(7), pos)

	_, err = cr.Seek(-1, io.SeekStart)
	assert.Error(t, err)
}

func TestCursorReader_ReadAt(t *testing.T) {
	t.Parallel()
	cr, _, _ := newCursorReaderForTest([]byte("0123456789"))
	t.Cleanup(func() { _ = cr.Close() })

	// Pre-advance cursor; ReadAt should NOT use it.
	_, err := cr.Seek(5, io.SeekStart)
	require.NoError(t, err)

	buf := make([]byte, 3)
	n, err := cr.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, "012", string(buf))

	pos, err := cr.Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	assert.Equal(t, int64(5), pos)
}

func TestCursorReader_ConcurrentReadAt(t *testing.T) {
	t.Parallel()
	data := make([]byte, 64*1024)
	for i := range data {
		data[i] = byte(i)
	}
	cr, _, _ := newCursorReaderForTest(data)
	t.Cleanup(func() { _ = cr.Close() })

	const workers, iters = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Go(func() {
			buf := make([]byte, 64)
			for i := 0; i < iters; i++ {
				off := int64((i * 271) % (len(data) - len(buf)))
				n, err := cr.ReadAt(buf, off)
				assert.NoError(t, err)
				assert.Equal(t, len(buf), n)
				for j, b := range buf {
					assert.Equal(t, byte(int(off)+j), b, "off=%d j=%d", off, j)
				}
			}
		})
	}
	wg.Wait()
}

func TestCursorReader_ConcurrentReadNoRace(t *testing.T) {
	t.Parallel()
	// Concurrent Read on a single cursorReader is documented as
	// semantically meaningless (interleaved bytes); the test only
	// asserts absence of data races under -race.
	cr, _, _ := newCursorReaderForTest(make([]byte, 64*1024))
	t.Cleanup(func() { _ = cr.Close() })

	const workers = 8
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Go(func() {
			buf := make([]byte, 64)
			for i := 0; i < 200; i++ {
				_, _ = cr.Read(buf)
			}
		})
	}
	wg.Wait()
}

func TestCursorReader_MixedReadAndReadAt(t *testing.T) {
	t.Parallel()
	data := make([]byte, 64*1024)
	for i := range data {
		data[i] = byte(i)
	}
	cr, _, _ := newCursorReaderForTest(data)
	t.Cleanup(func() { _ = cr.Close() })

	var wg sync.WaitGroup
	wg.Go(func() {
		buf := make([]byte, 32)
		for i := 0; i < 100; i++ {
			n, _ := cr.Read(buf)
			if n == 0 {
				break
			}
		}
	})
	for w := 0; w < 4; w++ {
		wg.Go(func() {
			buf := make([]byte, 32)
			for i := 0; i < 200; i++ {
				off := int64((i * 31) % (len(data) - len(buf)))
				n, err := cr.ReadAt(buf, off)
				assert.NoError(t, err)
				assert.Equal(t, len(buf), n)
				for j, b := range buf {
					assert.Equal(t, byte(int(off)+j), b)
				}
			}
		})
	}
	wg.Wait()
}

func TestCursorReader_CloseIdempotent(t *testing.T) {
	t.Parallel()
	cr, _, releases := newCursorReaderForTest([]byte("hello"))

	assert.NoError(t, cr.Close())
	assert.ErrorIs(t, cr.Close(), os.ErrClosed)
	assert.Equal(t, int32(1), releases.Load(), "release should be called exactly once")
}

func TestCursorReader_ReadAfterClose(t *testing.T) {
	t.Parallel()
	cr, _, _ := newCursorReaderForTest([]byte("hello"))
	require.NoError(t, cr.Close())

	buf := make([]byte, 5)
	_, err := cr.Read(buf)
	assert.ErrorIs(t, err, os.ErrClosed)

	_, err = cr.ReadAt(buf, 0)
	assert.ErrorIs(t, err, os.ErrClosed)
}

// TestCursorReader_SiblingCloseDoesNotDisturbReadAt verifies the
// documented safe pattern: Close on one cursorReader concurrent with
// ReadAt on a sibling cursorReader backed by the same SharedFile is
// fine. The sibling holds its own refcount, so the underlying file
// stays open until the sibling itself closes.
//
// This drives a real PackHandle (rather than a fake release
// callback) so the SharedFile refcount path is actually exercised.
func TestCursorReader_SiblingCloseDoesNotDisturbReadAt(t *testing.T) {
	t.Parallel()
	data := make([]byte, 64*1024)
	for i := range data {
		data[i] = byte(i)
	}

	fs := memfs.New()
	writeMemFile(t, fs, "p.pack", []byte("pack"))
	writeMemFile(t, fs, "p.idx", data)
	writeMemFile(t, fs, "p.rev", []byte("rev"))

	idxSrc, opens, _ := instrumentedSource(fs, "p.idx")
	ph := New(Sources{
		Pack: PathSource(fs, "p.pack"),
		Idx:  idxSrc,
		Rev:  PathSource(fs, "p.rev"),
	})
	defer ph.Close()

	// Two sibling cursorReaders share one SharedFile (one underlying
	// open). Closing one decrements the refcount but the other keeps
	// the FD alive for its own ReadAt. The idx reader exposes ReadAt
	// directly (RandomReader); the pack reader hides it by design.
	r1, err := ph.OpenIdxReader()
	require.NoError(t, err)
	r2, err := ph.OpenIdxReader()
	require.NoError(t, err)
	require.Equal(t, int32(1), opens.Load(), "siblings should share one open")

	// r2 runs ReadAt in a tight loop while r1 is being closed
	// concurrently. r2 must keep succeeding throughout.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 256)
		for {
			select {
			case <-stop:
				return
			default:
			}
			n, err := r2.ReadAt(buf, 0)
			assert.NoError(t, err)
			assert.Equal(t, len(buf), n)
			for j, b := range buf {
				if byte(j) != b {
					assert.Failf(t, "data mismatch", "j=%d got %d want %d", j, b, byte(j))
					return
				}
			}
		}
	}()

	// Hammer r1.Close from this goroutine. Close is idempotent: the
	// first call releases the refcount, the rest return os.ErrClosed.
	for i := 0; i < 1000; i++ {
		err := r1.Close()
		if i == 0 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, os.ErrClosed)
		}
	}

	close(stop)
	<-done

	// r2's ReadAt must still work after r1 is fully closed — the
	// underlying file is alive on r2's refcount.
	buf := make([]byte, 8)
	n, err := r2.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, len(buf), n)
	require.NoError(t, r2.Close())

	// One open total: siblings shared the underlying file throughout.
	assert.Equal(t, int32(1), opens.Load())
}
