package idxfile

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	billy "github.com/go-git/go-billy/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackingReaderAt is a billy.ReaderAtCloser backed by a bytes.Reader
// that tracks Close.
type trackingReaderAt struct {
	*bytes.Reader
	closed atomic.Bool
}

func (f *trackingReaderAt) ReadAt(p []byte, off int64) (int, error) { return f.Reader.ReadAt(p, off) }
func (f *trackingReaderAt) Close() error                            { f.closed.Store(true); return nil }

var _ billy.ReaderAtCloser = (*trackingReaderAt)(nil)

// newTestSharedFile creates a SharedFile with zero grace period for
// deterministic test behaviour.
func newTestSharedFile(opener func() (billy.ReaderAtCloser, error)) *SharedFile {
	sf := NewSharedFile(opener)
	sf.gracePeriod = 0
	return sf
}

func TestSharedFileAcquireRelease(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	tf := &trackingReaderAt{Reader: bytes.NewReader([]byte("hello"))}
	opener := func() (billy.ReaderAtCloser, error) {
		opens.Add(1)
		tf.closed.Store(false)
		return tf, nil
	}

	sf := newTestSharedFile(opener)

	r1, err := sf.Acquire()
	require.NoError(t, err)
	assert.Equal(t, int32(1), opens.Load())
	assert.False(t, tf.closed.Load())

	r2, err := sf.Acquire()
	require.NoError(t, err)
	assert.Equal(t, int32(1), opens.Load()) // no second open
	assert.Same(t, r1, r2)                  // same underlying file

	// Release doesn't close (refs still > 0).
	sf.Release()
	assert.False(t, tf.closed.Load())

	// Release closes (refs == 0).
	sf.Release()
	assert.True(t, tf.closed.Load())

	// Next acquire reopens.
	_, err = sf.Acquire()
	require.NoError(t, err)
	assert.Equal(t, int32(2), opens.Load())
	sf.Release()
}

func TestSharedFileCloseWithActiveReaders(t *testing.T) {
	t.Parallel()
	tf := &trackingReaderAt{Reader: bytes.NewReader([]byte("hello"))}
	opener := func() (billy.ReaderAtCloser, error) {
		tf.closed.Store(false)
		return tf, nil
	}

	sf := newTestSharedFile(opener)

	// Acquire a reference.
	r, err := sf.Acquire()
	require.NoError(t, err)
	require.NotNil(t, r)

	// Close while reader is active — file stays open.
	err = sf.Close()
	require.NoError(t, err)
	assert.False(t, tf.closed.Load())

	// Further acquires are rejected.
	_, err = sf.Acquire()
	assert.ErrorIs(t, err, ErrSharedFileClosed)

	// Active reader can still read.
	buf := make([]byte, 5)
	n, err := r.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, 5, n)

	// Release closes the file.
	sf.Release()
	assert.True(t, tf.closed.Load())
}

func TestSharedFileConcurrent(t *testing.T) {
	t.Parallel()
	data := []byte("concurrent test data for shared file")
	opener := func() (billy.ReaderAtCloser, error) {
		return &trackingReaderAt{Reader: bytes.NewReader(data)}, nil
	}

	sf := newTestSharedFile(opener)
	defer sf.Close()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			r, err := sf.Acquire()
			if err != nil {
				return
			}
			defer sf.Release()

			buf := make([]byte, 4)
			_, _ = r.ReadAt(buf, 0)
		}()
	}

	wg.Wait()

	// After all goroutines finish, file should be closed (refs == 0).
	sf.mu.Lock()
	assert.Nil(t, sf.file)
	assert.Equal(t, 0, sf.refs)
	sf.mu.Unlock()
}

func TestSharedFileCloseIdempotent(t *testing.T) {
	t.Parallel()
	opener := func() (billy.ReaderAtCloser, error) {
		return &trackingReaderAt{Reader: bytes.NewReader(nil)}, nil
	}

	sf := newTestSharedFile(opener)

	// Acquire and release so file gets opened then closed.
	_, err := sf.Acquire()
	require.NoError(t, err)
	sf.Release()

	// Multiple Close calls should be safe.
	assert.NoError(t, sf.Close())
	assert.NoError(t, sf.Close())
}

func TestSharedFileOpenerError(t *testing.T) {
	t.Parallel()
	opener := func() (billy.ReaderAtCloser, error) {
		return nil, io.ErrUnexpectedEOF
	}

	sf := newTestSharedFile(opener)

	_, err := sf.Acquire()
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestSharedFileReleaseUnderflow(t *testing.T) {
	t.Parallel()
	opener := func() (billy.ReaderAtCloser, error) {
		return &trackingReaderAt{Reader: bytes.NewReader(nil)}, nil
	}

	sf := newTestSharedFile(opener)

	assert.NotPanics(t, func() {
		sf.Release()
	})

	_, err := sf.Acquire()
	require.NoError(t, err)
	sf.Release()
	assert.NotPanics(t, func() {
		sf.Release()
	})
}

func TestSharedFileGracePeriod(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	tf := &trackingReaderAt{Reader: bytes.NewReader([]byte("grace"))}
	opener := func() (billy.ReaderAtCloser, error) {
		opens.Add(1)
		tf.closed.Store(false)
		return tf, nil
	}

	sf := NewSharedFile(opener)
	sf.gracePeriod = 50 * time.Millisecond

	// First acquire + release: file stays open during grace period.
	_, err := sf.Acquire()
	require.NoError(t, err)
	sf.Release()
	assert.False(t, tf.closed.Load(), "file should stay open during grace period")

	// Second acquire within grace period reuses the same FD.
	_, err = sf.Acquire()
	require.NoError(t, err)
	assert.Equal(t, int32(1), opens.Load(), "should reuse FD within grace period")
	sf.Release()

	// Wait for grace period to expire.
	time.Sleep(100 * time.Millisecond)
	assert.True(t, tf.closed.Load(), "file should close after grace period")

	// Next acquire reopens.
	_, err = sf.Acquire()
	require.NoError(t, err)
	assert.Equal(t, int32(2), opens.Load())
	sf.Release()

	_ = sf.Close()
}

func TestSharedFileGracePeriodResetByAcquire(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	tf := &trackingReaderAt{Reader: bytes.NewReader([]byte("reset"))}
	opener := func() (billy.ReaderAtCloser, error) {
		opens.Add(1)
		tf.closed.Store(false)
		return tf, nil
	}

	sf := NewSharedFile(opener)
	// The grace period needs to be wide enough that the "still open"
	// assertion below has comfortable margin over the time.Sleep
	// granularity on Windows (~15ms with significant jitter under CI
	// load). 200ms minus 50ms gives ~150ms of headroom.
	sf.gracePeriod = 200 * time.Millisecond

	_, err := sf.Acquire()
	require.NoError(t, err)
	sf.Release()
	assert.False(t, tf.closed.Load())

	// Acquire within grace period reuses the FD and resets the timer.
	time.Sleep(50 * time.Millisecond)
	_, err = sf.Acquire()
	require.NoError(t, err)
	assert.Equal(t, int32(1), opens.Load(), "should reuse FD, not reopen")
	sf.Release()

	// 50ms into the new 200ms grace period: file still open.
	time.Sleep(50 * time.Millisecond)
	assert.False(t, tf.closed.Load(), "new grace period hasn't expired")

	// After the new grace period expires: file closed. Poll instead of
	// sleeping a fixed amount so we don't race timer scheduling jitter.
	assert.Eventually(t, tf.closed.Load, time.Second, 10*time.Millisecond,
		"file should close after grace period")
	assert.Equal(t, int32(1), opens.Load())

	_ = sf.Close()
}

func TestSharedFileGracePeriodCancelledByClose(t *testing.T) {
	t.Parallel()
	tf := &trackingReaderAt{Reader: bytes.NewReader([]byte("cancel"))}
	opener := func() (billy.ReaderAtCloser, error) {
		tf.closed.Store(false)
		return tf, nil
	}

	sf := NewSharedFile(opener)
	sf.gracePeriod = time.Minute // long grace period

	_, err := sf.Acquire()
	require.NoError(t, err)
	sf.Release()
	assert.False(t, tf.closed.Load())

	// Close cancels the grace timer and closes immediately.
	require.NoError(t, sf.Close())
	assert.True(t, tf.closed.Load())
}
