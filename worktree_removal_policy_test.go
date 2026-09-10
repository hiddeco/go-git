package git

import (
	"fmt"
	"os"
	"path"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/stretchr/testify/require"
)

// snapshotSubtree includes every directory and every file's contents.
func snapshotSubtree(t *testing.T, fs billy.Filesystem, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	var walk func(string)
	walk = func(p string) {
		fi, err := fs.Lstat(p)
		require.NoError(t, err)
		if !fi.IsDir() {
			data, err := util.ReadFile(fs, p)
			require.NoError(t, err)
			snapshot[p] = string(data)
			return
		}
		snapshot[p+"/"] = ""
		children, err := fs.ReadDir(p)
		require.NoError(t, err)
		for _, child := range children {
			walk(path.Join(p, child.Name()))
		}
	}
	walk(root)
	return snapshot
}

func TestRemovalPreservesGitlinkSubtree(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"memfs", "osfs"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			raw := memfs.New()
			if backend == "osfs" {
				raw = osfs.New(t.TempDir())
			}
			wrapped := newWorktreeFilesystem(raw, true, false)
			for name, content := range map[string]string{"sub/.git": "gitdir: ../.git/modules/sub\n", "sub/a.txt": "tracked", "sub/dirty.txt": "uncommitted", "sub/nested/b.txt": "nested"} {
				require.NoError(t, util.WriteFile(raw, name, []byte(content), 0o644))
			}
			expected := map[string]string{"sub/": "", "sub/.git": "gitdir: ../.git/modules/sub\n", "sub/a.txt": "tracked", "sub/dirty.txt": "uncommitted", "sub/nested/": "", "sub/nested/b.txt": "nested"}
			require.Equal(t, expected, snapshotSubtree(t, raw, "sub"))
			require.NoError(t, rmFileAndDirsIfEmpty(wrapped, "sub"))
			require.Equal(t, expected, snapshotSubtree(t, raw, "sub"))
			require.NoError(t, util.WriteFile(raw, "p/q/only.txt", []byte("remove"), 0o644))
			require.NoError(t, rmFileAndDirsIfEmpty(wrapped, "p/q/only.txt"))
			_, err := raw.Lstat("p")
			require.ErrorIs(t, err, os.ErrNotExist)
			require.NoError(t, raw.MkdirAll("empty", 0o755))
			require.NoError(t, rmFileAndDirsIfEmpty(wrapped, "empty"))
			_, err = raw.Lstat("empty")
			require.ErrorIs(t, err, os.ErrNotExist)
			require.NoError(t, rmFileAndDirsIfEmpty(wrapped, "missing"))
			require.NoError(t, util.WriteFile(raw, "tracked.txt/precious", []byte("keep"), 0o644))
			require.NoError(t, rmFileAndDirsIfEmpty(wrapped, "tracked.txt"))
			require.Equal(t, map[string]string{"tracked.txt/": "", "tracked.txt/precious": "keep"}, snapshotSubtree(t, raw, "tracked.txt"))
		})
		for _, name := range []string{".git", "a/.git/b"} {
			for _, directory := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/refuse/%q/directory=%t", backend, name, directory), func(t *testing.T) {
					t.Parallel()
					raw := memfs.New()
					if backend == "osfs" {
						raw = osfs.New(t.TempDir())
					}
					p := name
					if directory {
						p += "/inner.txt"
					}
					require.NoError(t, util.WriteFile(raw, p, []byte("keep"), 0o644))
					before := snapshotSubtree(t, raw, name)
					require.Error(t, rmFileAndDirsIfEmpty(newWorktreeFilesystem(raw, true, false), name))
					require.Equal(t, before, snapshotSubtree(t, raw, name))
				})
			}
		}
	}
}
