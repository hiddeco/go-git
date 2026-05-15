package idxfile

import (
	"errors"
	"io"
	"sync"
	"time"

	billy "github.com/go-git/go-billy/v6"
)

const defaultCloseGracePeriod = time.Second

// SharedFile provides shared, reference-counted access to a file that is
// opened lazily and closed automatically when no readers remain.
//
// Multiple goroutines can acquire the file concurrently; they all share
// a single underlying file descriptor. The FD is opened on first
// Acquire and closed after a grace period once the last active
// reference is released. This avoids holding file descriptors open
// indefinitely — which causes problems on Windows, where open files
// cannot be deleted — while still sharing a single FD across concurrent
// readers and avoiding repeated open/close syscalls for sequential
// operations.
//
// The opener MUST return a billy.ReaderAtCloser whose ReadAt is
// concurrent-safe across goroutines (the standard contract for
// io.ReaderAt and the documented contract for billy.ReaderAtFS).
// SharedFile does not synchronise concurrent ReadAt calls itself.
//
// All methods are safe for concurrent use.
type SharedFile struct {
	opener      func() (billy.ReaderAtCloser, error)
	gracePeriod time.Duration

	mu         sync.Mutex
	file       billy.ReaderAtCloser
	refs       int
	closed     bool
	closeTimer *time.Timer
	timerGen   uint64 // incremented each time a timer is stopped/replaced
}

// ErrSharedFileClosed is returned by Acquire when called on a Closed
// SharedFile, and is wrapped by PackHandle.OpenXReader for the same
// case. Callers test with errors.Is.
var ErrSharedFileClosed = errors.New("shared file is closed")

// NewSharedFile constructs a SharedFile that lazily opens its
// underlying handle via opener. The opener is invoked on first Acquire
// and again after each grace-period close.
func NewSharedFile(opener func() (billy.ReaderAtCloser, error)) *SharedFile {
	return &SharedFile{opener: opener, gracePeriod: defaultCloseGracePeriod}
}

// Acquire increments the reference count and returns the underlying
// file as an io.ReaderAt. If the file is not currently open, it is
// opened via the opener function.
//
// The caller MUST call Release when done reading. Failing to do so
// prevents the file descriptor from ever closing. Acquire returns
// ErrSharedFileClosed if the SharedFile has been permanently closed.
func (sf *SharedFile) Acquire() (io.ReaderAt, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	if sf.closed {
		return nil, ErrSharedFileClosed
	}

	// Cancel any pending grace-period close.
	if sf.closeTimer != nil {
		sf.closeTimer.Stop()
		sf.closeTimer = nil
		sf.timerGen++ // invalidate any already-queued timer callback
	}

	if sf.file == nil {
		f, err := sf.opener()
		if err != nil {
			return nil, err
		}
		sf.file = f
	}

	sf.refs++
	return sf.file, nil
}

// Release decrements the reference count. When it reaches zero the
// underlying file is closed after a grace period (or immediately if
// the SharedFile has been permanently closed).
func (sf *SharedFile) Release() {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	if sf.refs <= 0 {
		return
	}

	sf.refs--
	if sf.refs > 0 {
		return
	}

	// refs == 0: schedule (or perform) the close.
	if sf.closed || sf.gracePeriod == 0 {
		sf.closeLocked()
		return
	}

	gen := sf.timerGen
	sf.closeTimer = time.AfterFunc(sf.gracePeriod, func() {
		sf.mu.Lock()
		defer sf.mu.Unlock()
		if sf.timerGen == gen && sf.refs == 0 && sf.file != nil {
			sf.closeLocked()
		}
	})
}

// Close marks the SharedFile as permanently closed, preventing future
// Acquire calls. If no references are active the underlying file is
// closed immediately; otherwise it closes when the last active
// reference is released. Terminal Close bypasses the grace timer.
// Close is idempotent and safe to call concurrently.
func (sf *SharedFile) Close() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	sf.closed = true
	if sf.closeTimer != nil {
		sf.closeTimer.Stop()
		sf.closeTimer = nil
		sf.timerGen++ // invalidate any already-queued timer callback
	}
	if sf.refs == 0 && sf.file != nil {
		err := sf.file.Close()
		sf.file = nil
		return err
	}
	return nil
}

// closeLocked closes the underlying file. Must be called with mu held.
func (sf *SharedFile) closeLocked() {
	if sf.file != nil {
		_ = sf.file.Close()
		sf.file = nil
	}
	if sf.closeTimer != nil {
		sf.closeTimer.Stop()
		sf.closeTimer = nil
	}
}
