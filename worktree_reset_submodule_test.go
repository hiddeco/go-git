package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/plumbing"
)

func pathPolicyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "protocol.file.allow=always", "-c", "submodule.recurse=false", "-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %q: %s", args, out)
	return strings.TrimSpace(string(out))
}

func TestResetPreservesSubmoduleDirectory(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	for _, implementation := range []string{"go-git", "git"} {
		t.Run(implementation, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source, parent := filepath.Join(root, "source"), filepath.Join(root, "parent")
			for _, dir := range []string{source, parent} {
				require.NoError(t, os.Mkdir(dir, 0o755))
				pathPolicyGit(t, dir, "init", "-q")
			}
			write := func(dir, name, content string) {
				t.Helper()
				p := filepath.Join(dir, filepath.FromSlash(name))
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
			}
			write(source, "a.txt", "tracked\n")
			write(source, "nested/b.txt", "nested\n")
			pathPolicyGit(t, source, "add", ".")
			pathPolicyGit(t, source, "commit", "-qm", "source")
			write(parent, "safe.txt", "keep\n")
			pathPolicyGit(t, parent, "add", ".")
			pathPolicyGit(t, parent, "commit", "-qm", "base")
			base := plumbing.NewHash(pathPolicyGit(t, parent, "rev-parse", "HEAD"))
			pathPolicyGit(t, parent, "submodule", "add", "-q", source, "sub")
			write(parent, "removed.txt", "remove\n")
			pathPolicyGit(t, parent, "add", ".")
			pathPolicyGit(t, parent, "commit", "-qm", "submodule")
			write(parent, "sub/a.txt", "dirty tracked\n")
			write(parent, "sub/dirty.txt", "untracked work\n")
			write(parent, "sub/nested/extra.txt", "nested untracked\n")
			require.Equal(t, "true", pathPolicyGit(t, filepath.Join(parent, "sub"), "rev-parse", "--is-inside-work-tree"))
			raw := osfs.New(parent)
			before := snapshotSubtree(t, raw, "sub")
			require.Len(t, before, 7)
			r, err := PlainOpen(parent)
			require.NoError(t, err)
			w, err := r.Worktree()
			require.NoError(t, err)
			if implementation == "git" {
				pathPolicyGit(t, parent, "reset", "--hard", base.String())
			} else {
				require.NoError(t, w.Reset(&ResetOptions{Mode: HardReset, Commit: base}))
			}
			require.Equal(t, before, snapshotSubtree(t, raw, "sub"))
			for _, name := range []string{"removed.txt", ".gitmodules"} {
				_, err := raw.Lstat(name)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			require.Equal(t, "?? sub/", pathPolicyGit(t, parent, "status", "--porcelain"))
			require.Empty(t, pathPolicyGit(t, parent, "clean", "-fdxn"))
			if implementation == "git" {
				require.Empty(t, pathPolicyGit(t, parent, "clean", "-fdx"))
				require.Equal(t, before, snapshotSubtree(t, raw, "sub"))
			} else {
				// Clean's nested-repository behavior is deferred: reset preservation
				// does not promise preservation through the subsequent Clean.
				status, err := w.Status()
				require.NoError(t, err)
				require.Len(t, status, 4)
				for _, name := range []string{"sub/a.txt", "sub/dirty.txt", "sub/nested/b.txt", "sub/nested/extra.txt"} {
					require.Equal(t, &FileStatus{Staging: Untracked, Worktree: Untracked}, status[name])
				}
				require.NoError(t, w.Clean(&CleanOptions{Dir: true}))
				require.Equal(t, map[string]string{"sub/": "", "sub/.git": before["sub/.git"]}, snapshotSubtree(t, raw, "sub"))
			}
		})
	}
}
