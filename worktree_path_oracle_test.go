package git

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/internal/pathutil"
)

//nolint:paralleltest // Subtests share the Git index and config.
func TestPathPolicyMatchesGitIndex(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("POSIX Git oracle; native Windows gates have separate tests")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	dir := t.TempDir()
	pathPolicyGit(t, dir, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "blob"), []byte("content"), 0o644))
	blob := pathPolicyGit(t, dir, "hash-object", "-w", "blob")
	// Divergences are explicit expected verdicts, independent of the predicates.
	cases := []struct{ name, rule string }{
		{".git", "literal"},
		{"sub/.git", "literal"},
		{".GIT", "literal"},
		{"sub/.GIT", "literal"},
		{". /.git", "literal"},
		{"::$INDEX_ALLOCATION/.git", "literal"},
		{":a/.git", "literal"},
		{"git~1", "shortname"},
		{"git~1/HEAD", "shortname"},
		{"git~1 ", "ntfs"},
		{"sub/git~1 ", "ntfs"},
		{".git ", "ntfs"},
		{".git::$INDEX_ALLOCATION", "ntfs"},
		{"a\\.git", "backslash"},
		{".git\u200c", "hfs"},
		{"sub/.git\u200c", "hfs"},
		{".gi\u200ct", "hfs"},
		{".g\u200cit", "hfs"},
		{".\u200cgit", "hfs"},
		{"\u200c.git", "hfs"},
		{".. ", "tree-policy"},
		{"..:x", "tree-policy"},
		{".. .", "tree-policy"},
		{"x/.. ", "tree-policy"},
		{"..::$INDEX_ALLOCATION", "tree-policy"},
		{".\u200c./inner.txt", "tree-policy"},
		{"x/.\u200c.", "tree-policy"},
		{"tab\tname", "tree-policy"},
		{"del\x7fname", "tree-policy"},
		{"a\\.", "tree-policy"},
		{"a\\..", "tree-policy"},
		{"trail.", "ordinary"},
		{"trail ", "ordinary"},
		{".../inner.txt", "ordinary"},
		{"....", "ordinary"},
		{"sub /x", "ordinary"},
		{".gitattributes ", "ordinary"},
		{".gitignore ", "ordinary"},
		{".gitmodules ", "ordinary"},
		{".mailmap ", "ordinary"},
		{".GITIGNORE", "ordinary"},
		{"aux.c", "ordinary"},
		{"lib/con.go", "ordinary"},
		{"con c", "ordinary"},
		{"C:foo", "ordinary"},
		{"a:b", "ordinary"},
		{"C:/x", "ordinary"},
		{`\\srv\share\x`, "ordinary"},
		{`\??\C:\x`, "ordinary"},
	}
	for _, ntfs := range []bool{false, true} {
		for _, hfs := range []bool{false, true} {
			pathPolicyGit(t, dir, "config", "core.protectNTFS", fmt.Sprint(ntfs))
			pathPolicyGit(t, dir, "config", "core.protectHFS", fmt.Sprint(hfs))
			fs := newWorktreeFilesystem(memfs.New(), ntfs, hfs)
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%q/ntfs=%t/hfs=%t", tc.name, ntfs, hfs), func(t *testing.T) {
					pathPolicyGit(t, dir, "read-tree", "--empty")
					cmd := exec.Command("git", "-C", dir, "update-index", "--add", "--cacheinfo", "100644", blob, tc.name)
					_, _ = cmd.CombinedOutput()
					indexed := pathPolicyGit(t, dir, "ls-files", "-z")
					gitAccepts := tc.rule != "literal" &&
						((!ntfs || (tc.rule != "ntfs" && tc.rule != "shortname" && tc.rule != "backslash")) &&
							(!hfs || tc.rule != "hfs"))
					require.Equal(t, gitAccepts, indexed == tc.name+"\x00", "Git index: %q", indexed)
					// "shortname" is the bare 8.3 alias git~1, which Git
					// carries in the index with core.protectNTFS off and
					// go-git refuses whatever the configuration says.
					worktreeAccepts := gitAccepts && tc.rule != "tree-policy" &&
						tc.rule != "backslash" && tc.rule != "shortname"
					require.Equal(t, worktreeAccepts, fs.validPath(tc.name) == nil)
					require.Equal(t, worktreeAccepts, fs.validWritePath(tc.name) == nil)
					require.Equal(t, tc.rule == "ordinary", pathutil.ValidTreePath(tc.name) == nil)
				})
			}
		}
	}
}

func TestPlainClonePOSIXPathPolicy(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("POSIX filenames and Linux config defaults")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	for _, tc := range []struct {
		name     string
		rejected bool
	}{
		{"trail.", false},
		{"trail ", false},
		{".../inner.txt", false},
		{". /inner.txt", false},
		{".\u200c/inner.txt", false},
		{"aux.c", false},
		{"lib/con.go", false},
		{"a:b.txt", false},
		{".gitattributes ", false},
		{"gi7eba~1", false},
		{".. ", true},
		{"..:x", true},
		{".. .", true},
		{"x/.. ", true},
		{"..::$INDEX_ALLOCATION", true},
		{".\u200c./inner.txt", true},
		{"x/.\u200c.", true},
		{"tab\tname", true},
		{"del\x7fname", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(root, "source")
			require.NoError(t, os.Mkdir(source, 0o755))
			pathPolicyGit(t, source, "init", "-q")
			p := filepath.Join(source, tc.name)
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte("payload"), 0o644))
			pathPolicyGit(t, source, "add", "--all")
			require.Equal(t, tc.name+"\x00", pathPolicyGit(t, source, "ls-files", "-z"))
			pathPolicyGit(t, source, "commit", "-qm", "fixture")
			oracle := filepath.Join(root, "git-clone")
			pathPolicyGit(t, source, "clone", "-q", source, oracle)
			content, err := os.ReadFile(filepath.Join(oracle, tc.name))
			require.NoError(t, err)
			require.Equal(t, "payload", string(content))
			r, err := PlainOpen(source)
			require.NoError(t, err)
			head, err := r.Head()
			require.NoError(t, err)
			commit, err := r.CommitObject(head.Hash())
			require.NoError(t, err)
			tree, err := commit.Tree()
			require.NoError(t, err)
			iter := tree.Files()
			defer iter.Close()
			file, walkErr := iter.Next()
			clone := filepath.Join(root, "go-clone")
			_, cloneErr := PlainClone(clone, &CloneOptions{URL: source})
			if tc.rejected {
				// This single-offender fixture yields nothing. Mixed trees may yield
				// earlier files before the error; full iteration remains unavailable.
				require.Nil(t, file)
				require.Error(t, walkErr)
				require.NotErrorIs(t, walkErr, io.EOF)
				require.Error(t, cloneErr)
			} else {
				require.NoError(t, walkErr)
				require.Equal(t, tc.name, file.Name)
				_, err = iter.Next()
				require.ErrorIs(t, err, io.EOF)
				require.NoError(t, cloneErr)
				content, err := os.ReadFile(filepath.Join(clone, tc.name))
				require.NoError(t, err)
				require.Equal(t, "payload", string(content))
			}
		})
	}
}

func TestGitReportsFilteredUntrackedNames(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("literal POSIX names")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	for _, name := range []string{"git~1", ".GIT", ".git ", ".git.", ".git::$DATA", "GIT~1 ", ".git\u200c", "sub/.GIT", "sub/git~1", "sub/.git\u200c"} {
		for _, directory := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/directory=%t", name, directory), func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				pathPolicyGit(t, dir, "init", "-q")
				p := name
				if directory {
					p += "/inner.txt"
				}
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, p), []byte("payload"), 0o644))
				for _, ntfs := range []bool{false, true} {
					for _, hfs := range []bool{false, true} {
						pathPolicyGit(t, dir, "config", "core.protectNTFS", fmt.Sprint(ntfs))
						pathPolicyGit(t, dir, "config", "core.protectHFS", fmt.Sprint(hfs))
						require.NotEmpty(t, pathPolicyGit(t, dir, "clean", "-fdxn"))
						reported := pathPolicyGit(t, dir, "status", "--porcelain", "-z", "--untracked-files=all")
						require.Equal(t, "?? "+p+"\x00", reported)
					}
				}
				// Unlike go-git's config-matched filters, Git removes these names.
				require.True(t, strings.Contains(pathPolicyGit(t, dir, "clean", "-fdx"), "Removing"))
				_, err := os.Stat(filepath.Join(dir, p))
				require.ErrorIs(t, err, os.ErrNotExist)
			})
		}
	}
}
