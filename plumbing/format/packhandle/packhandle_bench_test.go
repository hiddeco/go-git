package packhandle_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"
	fixtures "github.com/go-git/go-git-fixtures/v6"

	"github.com/go-git/go-git/v6/plumbing/format/packhandle"
)

// Source policy:
//
//   - B1 (BenchmarkPackHandleAcquireGrace) and B3 (BenchmarkPackHandleMeta)
//     use the fixtures embed.FS via packhandle.PathSource. embedfs does not
//     implement billy.ReaderAtFS with mmap; util.OpenReaderAt falls back to
//     a serialised wrapper. That's fine for these benchmarks because they
//     measure the cursorReader/SharedFile/Meta paths, not concurrent ReadAt
//     scaling.
//
//   - B2 (BenchmarkPackHandleParallelReadAt) deliberately copies the pack
//     triple onto an osfs.New(b.TempDir()) so util.OpenReaderAt selects the
//     mmap path. The benchmark validates the spec's claim that concurrent
//     ReadAt against one SharedFile scales near-linearly on mmap-backed
//     filesystems, so we need the real mmap implementation in the loop.

// fixturePackStem returns the "data/pack-<hash>" stem of the basic fixture
// inside the fixtures embed.FS.
func fixturePackStem(b *testing.B) string {
	b.Helper()
	return fmt.Sprintf("data/pack-%s", fixtures.Basic().One().PackfileHash)
}

// newEmbedFixturePackHandle constructs a PackHandle whose Sources read the
// basic fixture's pack triple directly from the fixtures embed.FS.
func newEmbedFixturePackHandle(b *testing.B) *packhandle.PackHandle {
	b.Helper()
	stem := fixturePackStem(b)
	return packhandle.New(packhandle.Sources{
		Pack: packhandle.PathSource(fixtures.Filesystem, stem+".pack"),
		Idx:  packhandle.PathSource(fixtures.Filesystem, stem+".idx"),
		Rev:  packhandle.PathSource(fixtures.Filesystem, stem+".rev"),
	})
}

// copyFixturePackToTempDir copies the basic fixture's .pack/.idx/.rev into
// b.TempDir() and returns the directory plus the on-disk pack-file base name.
// The destination is an osfs so util.OpenReaderAt activates the mmap path.
func copyFixturePackToTempDir(b *testing.B) (dir string, pack, idx, rev string) {
	b.Helper()
	dir = b.TempDir()
	stem := fixturePackStem(b)
	dstFS := osfs.New(dir)

	for _, ext := range []string{".pack", ".idx", ".rev"} {
		src, err := fixtures.Filesystem.Open(stem + ext)
		if err != nil {
			b.Fatalf("open fixture %s%s: %v", stem, ext, err)
		}
		data, err := io.ReadAll(src)
		_ = src.Close()
		if err != nil {
			b.Fatalf("read fixture %s%s: %v", stem, ext, err)
		}
		name := filepath.Base(stem) + ext
		if err := util.WriteFile(dstFS, name, data, 0o644); err != nil {
			b.Fatalf("write %s: %v", name, err)
		}
	}
	base := filepath.Base(stem)
	return dir, base + ".pack", base + ".idx", base + ".rev"
}

// packSizeFromFixture returns the on-disk size of the basic fixture's pack
// file, sourced from the fixtures embed.FS.
func packSizeFromFixture(b *testing.B) int64 {
	b.Helper()
	stem := fixturePackStem(b)
	info, err := fixtures.Filesystem.Stat(stem + ".pack")
	if err != nil {
		b.Fatalf("stat fixture pack: %v", err)
	}
	return info.Size()
}

// BenchmarkPackHandleAcquireGrace measures the steady-state cost of the
// OpenPackReader -> ReadAt -> Close cycle against a warm PackHandle. The
// 1-second grace period should keep the underlying SharedFile open across
// iterations, so the loop pays only cursorReader allocation, the
// SharedFile acquire/release mutex round-trip, and a single 64-byte ReadAt
// — never a fresh open(2).
func BenchmarkPackHandleAcquireGrace(b *testing.B) {
	ph := newEmbedFixturePackHandle(b)
	b.Cleanup(func() { _ = ph.Close() })

	packSize := packSizeFromFixture(b)
	validRange := packSize - 64
	if validRange <= 0 {
		b.Fatalf("pack too small: %d bytes", packSize)
	}

	// Warm the SharedFile so the first timed iteration also benefits
	// from the grace-period reuse.
	warm, err := ph.OpenPackReader()
	if err != nil {
		b.Fatal(err)
	}
	_ = warm.Close()

	buf := make([]byte, 64)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := ph.OpenPackReader()
		if err != nil {
			b.Fatal(err)
		}
		off := int64(i) % validRange
		if _, err := r.(io.ReaderAt).ReadAt(buf, off); err != nil && err != io.EOF {
			b.Fatal(err)
		}
		_ = r.Close()
	}
}

// BenchmarkPackHandleParallelReadAt measures concurrent ReadAt against one
// PackHandle (sibling cursorReaders, one per goroutine) versus a direct
// (*os.File).ReadAt ceiling on the same underlying file. The spec claims
// near-linear scaling on mmap-backed filesystems; the baseline shows the
// mmap pread ceiling for comparison. Run with `-cpu=1,2,4,8` to see the
// curve.
func BenchmarkPackHandleParallelReadAt(b *testing.B) {
	dir, packName, _, _ := copyFixturePackToTempDir(b)
	packPath := filepath.Join(dir, packName)

	info, err := os.Stat(packPath)
	if err != nil {
		b.Fatalf("stat pack: %v", err)
	}
	packSize := info.Size()
	validRange := packSize - 64
	if validRange <= 0 {
		b.Fatalf("pack too small: %d bytes", packSize)
	}

	b.Run("packhandle_readat", func(b *testing.B) {
		fsys := osfs.New(dir)
		ph := packhandle.New(packhandle.Sources{
			Pack: packhandle.PathSource(fsys, packName),
			Idx:  packhandle.PathSource(fsys, packName), // any path; idx not used
			Rev:  packhandle.PathSource(fsys, packName),
		})
		b.Cleanup(func() { _ = ph.Close() })

		// Pre-warm the SharedFile so the timed region begins with an
		// already-open mmap.
		warm, err := ph.OpenPackReader()
		if err != nil {
			b.Fatal(err)
		}
		_ = warm.Close()

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			r, err := ph.OpenPackReader()
			if err != nil {
				b.Fatal(err)
			}
			defer r.Close()
			ra := r.(io.ReaderAt)
			buf := make([]byte, 64)
			var i int64
			for pb.Next() {
				off := i % validRange
				if _, err := ra.ReadAt(buf, off); err != nil && err != io.EOF {
					b.Fatal(err)
				}
				i++
			}
		})
	})

	b.Run("baseline_direct_pread", func(b *testing.B) {
		f, err := os.OpenFile(packPath, os.O_RDONLY, 0)
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = f.Close() })

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			buf := make([]byte, 64)
			var i int64
			for pb.Next() {
				off := i % validRange
				if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
					b.Fatal(err)
				}
				i++
			}
		})
	})
}

// BenchmarkPackHandleMeta measures PackHandle.Meta(20). The "first"
// sub-benchmark constructs a fresh PackHandle per iteration and pays the
// cold cost: stat + acquire + two ReadAts + release. The "cached"
// sub-benchmark shares one PackHandle whose sync.Once has already fired,
// so each iteration is just a struct copy out of the OnceValues closure.
func BenchmarkPackHandleMeta(b *testing.B) {
	stem := fixturePackStem(b)
	packPath := stem + ".pack"
	idxPath := stem + ".idx"
	revPath := stem + ".rev"

	b.Run("first", func(b *testing.B) {
		// Sanity-check the fixture once outside the loop to avoid
		// burning iterations on a missing-file failure.
		if _, err := fixtures.Filesystem.Stat(packPath); err != nil {
			b.Fatalf("stat fixture pack: %v", err)
		}

		// Use an atomic to keep the result observable to the compiler
		// and prevent over-eager dead-store elimination.
		var sink atomic.Uint32

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ph := packhandle.New(packhandle.Sources{
				Pack: packhandle.PathSource(fixtures.Filesystem, packPath),
				Idx:  packhandle.PathSource(fixtures.Filesystem, idxPath),
				Rev:  packhandle.PathSource(fixtures.Filesystem, revPath),
			})
			m, err := ph.Meta(20)
			if err != nil {
				b.Fatal(err)
			}
			sink.Store(m.Count)
			_ = ph.Close()
		}
	})

	b.Run("cached", func(b *testing.B) {
		ph := newEmbedFixturePackHandle(b)
		b.Cleanup(func() { _ = ph.Close() })

		// Warm the sync.Once.
		if _, err := ph.Meta(20); err != nil {
			b.Fatal(err)
		}

		var sink atomic.Uint32

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m, err := ph.Meta(20)
			if err != nil {
				b.Fatal(err)
			}
			sink.Store(m.Count)
		}
	})
}
