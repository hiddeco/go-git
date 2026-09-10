package git

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/internal/pathutil"
	"github.com/go-git/go-git/v6/storage/memory"
)

func TestValidPathRejectsDotGitEveryPosition(t *testing.T) {
	t.Parallel()
	groups := []struct {
		name  string
		paths []string
	}{
		// `.git` and the bare 8.3 short name `git~1` are refused
		// whatever the configuration says, as ValidTreePath refuses
		// them. core.protectNTFS only adds the spellings NTFS
		// canonicalisation folds back to one of the two.
		{"always", []string{". /.git", ".\u200c/.git", ".../.git", "..../.git", " /.git", "::$INDEX_ALLOCATION/.git", ":a/.git", "a/. /.git", "sub/.git", "sub/.GIT", ".GIT", "a\\.git", "a/.git/config", "git~1", "git~1/HEAD", "sub/git~1", "GIT~1"}},
		{"ntfs", []string{"sub/GIT~1 ", ".git ", "git~1 ", ".git::$INDEX_ALLOCATION"}},
		{"hfs", []string{"sub/.git\u200c", "sub/.gi\u200ct", "sub/.g\u200cit", "sub/.\u200cgit", "sub/\u200c.git"}},
	}
	for _, group := range groups {
		for _, ntfs := range []bool{false, true} {
			for _, hfs := range []bool{false, true} {
				for _, p := range group.paths {
					t.Run(fmt.Sprintf("%s/%q/ntfs=%t/hfs=%t", group.name, p, ntfs, hfs), func(t *testing.T) {
						t.Parallel()
						fs := newWorktreeFilesystem(memfs.New(), ntfs, hfs)
						rejected := group.name == "always" || group.name == "ntfs" && ntfs || group.name == "hfs" && hfs
						for _, check := range []func(...string) error{fs.validPath, fs.validWritePath} {
							err := check(p)
							if rejected {
								require.Error(t, err)
							} else {
								require.NoError(t, err)
							}
						}
					})
				}
			}
		}
	}
}

func TestValidPathAcceptsPOSIXNamesOffWindows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path          string
		win32, volume bool
	}{
		{"trail.", true, false},
		{"trail ", true, false},
		{".../inner.txt", true, false},
		{"sub /x", true, false},
		{". /inner.txt", true, false},
		{".\u200c/inner.txt", false, false},
		{".gitattributes ", true, false},
		{".gitignore ", true, false},
		{".mailmap ", true, false},
		{"gi7eba~1", false, false},
		{"aux.c", true, false},
		{"lib/con.go", true, false},
		{"C:foo", false, true},
		{"a:b", false, true},
		{"C:/x", false, true},
		{`\\srv\share\x`, false, true},
		{`\??\C:\x`, false, true},
		{".gitattributes /inner.txt", true, false},
	} {
		for _, ntfs := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/ntfs=%t", tc.path, ntfs), func(t *testing.T) {
				t.Parallel()
				fs := newWorktreeFilesystem(memfs.New(), ntfs, false)
				err := fs.validPath(tc.path)
				if runtime.GOOS == "windows" && (tc.volume || ntfs && tc.win32) {
					require.Error(t, err)
					if !tc.volume {
						require.Contains(t, err.Error(), "core.protectNTFS")
					}
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

//nolint:paralleltest // Exercise Clean first, with a fresh fixture for each operation.
func TestWorktreeOperationsSurviveDotGitDisguises(t *testing.T) {
	t.Parallel()
	for _, name := range []string{".git", "git~1", ".GIT", "sub/.GIT", "sub/git~1", "sub/.git", ".git\u200c", "sub/.git\u200c"} {
		for _, directory := range []bool{false, true} {
			for _, op := range []string{"Clean", "Status", "AddUnrelated", "AddGlob", "AddAll", "AddName"} {
				t.Run(fmt.Sprintf("%q/directory=%t/%s", name, directory, op), func(t *testing.T) {
					// Keep Clean first; every operation starts with an untracked fixture.
					fs := memfs.New()
					r, err := Init(memory.NewStorage(), WithWorkTree(fs))
					require.NoError(t, err)
					cfg, err := r.Config()
					require.NoError(t, err)
					cfg.Core.ProtectNTFS = config.OptBoolTrue
					cfg.Core.ProtectHFS = config.OptBoolTrue
					require.NoError(t, r.SetConfig(cfg))
					w, err := r.Worktree()
					require.NoError(t, err)
					p := name
					if directory {
						p += "/inner.txt"
					}
					require.NoError(t, util.WriteFile(fs, p, []byte("preserved"), 0o644))
					require.NoError(t, util.WriteFile(fs, "unrelated.txt", []byte("safe"), 0o644))
					switch op {
					case "Clean":
						require.NoError(t, w.Clean(&CleanOptions{Dir: true}))
						body, err := util.ReadFile(fs, p)
						require.NoError(t, err)
						require.Equal(t, "preserved", string(body))
					case "Status":
						status, err := w.Status()
						require.NoError(t, err)
						require.Equal(t, Status{"unrelated.txt": &FileStatus{Staging: Untracked, Worktree: Untracked}}, status)
					case "AddUnrelated":
						_, err = w.Add("unrelated.txt")
						require.NoError(t, err)
					case "AddGlob":
						require.NoError(t, w.AddGlob("."))
					case "AddAll":
						require.NoError(t, w.AddWithOptions(&AddOptions{All: true}))
					case "AddName":
						_, err = w.Add(name)
						require.ErrorIs(t, err, pathutil.ErrInvalidPath)
					}
				})
			}
		}
	}
}

//nolint:paralleltest // Exercise Clean first, with a fresh fixture for each operation.
func TestWorktreeAPISurvivesUntrackedEdgeNames(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX filenames")
	}
	for _, name := range []string{"build ", ". ", ".\u200c", ".gitattributes ", ".gitignore ", ".mailmap ", "gi7eba~1", ".GITIGNORE ", "a:b", "aux.c"} {
		for _, directory := range []bool{false, true} {
			for _, op := range []string{"Clean", "Status", "AddUnrelated", "AddGlob", "AddAll", "AddName"} {
				t.Run(fmt.Sprintf("%q/directory=%t/%s", name, directory, op), func(t *testing.T) {
					fs := memfs.New()
					r, err := Init(memory.NewStorage(), WithWorkTree(fs))
					require.NoError(t, err)
					w, err := r.Worktree()
					require.NoError(t, err)
					p := name
					if directory {
						p += "/inner.txt"
					}
					require.NoError(t, util.WriteFile(fs, p, []byte("content"), 0o644))
					require.NoError(t, util.WriteFile(fs, "unrelated.txt", []byte("safe"), 0o644))
					switch op {
					case "Clean":
						require.NoError(t, w.Clean(&CleanOptions{Dir: true}))
						_, err = fs.Lstat(name)
						require.ErrorIs(t, err, os.ErrNotExist)
					case "Status":
						status, err := w.Status()
						require.NoError(t, err)
						require.Contains(t, status, p)
					case "AddUnrelated":
						_, err = w.Add("unrelated.txt")
						require.NoError(t, err)
					case "AddGlob":
						require.NoError(t, w.AddGlob("."))
					case "AddAll":
						require.NoError(t, w.AddWithOptions(&AddOptions{All: true}))
					case "AddName":
						_, err = w.Add(name)
						require.NoError(t, err)
					}
				})
			}
		}
	}
}
