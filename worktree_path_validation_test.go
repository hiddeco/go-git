package git

import (
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// pathTestCase describes a tree to craft for path-validation tests.
// path is the leaf entry's tree path (nested paths use '/' to build
// nested tree objects). mode and content describe the leaf entry \u2014
// for symlinks, content is the raw symlink target.
type pathTestCase struct {
	name    string
	path    string
	mode    filemode.FileMode
	content []byte
	config  map[string]string
	wantErr bool
}

// TestPathValidationRejectsDangerousResets verifies that go-git rejects
// resetting onto commits that contain dangerous paths. For each case, a
// commit is crafted with a tree containing a single bad path, then
// Reset(HardReset) is run against it; the diff machinery's per-change
// validation must reject before any worktree write happens.
//
// The upstream `git reset --hard` is not used as a comparator because
// its checkout path does not run verify_path on existing commits the
// way cherry-pick or merge do; v5 does not expose those operations on
// the Worktree. Conformance with upstream Git's path rules is verified
// indirectly via TestValidPath / TestWindowsValidPath.
func TestPathValidationRejectsDangerousResets(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping path validation conformance test in short mode")
	}
	t.Parallel()

	tests := []pathTestCase{
		{
			name:    ".git at root",
			path:    ".git/config",
			mode:    filemode.Regular,
			content: []byte("payload"),
			wantErr: true,
		},
		{
			name:    ".git in subdirectory",
			path:    "subdir/.git/config",
			mode:    filemode.Regular,
			content: []byte("payload"),
			wantErr: true,
		},
		{
			name:    ".git as final-position regular file",
			path:    "submodule/.git",
			mode:    filemode.Regular,
			content: []byte("payload"),
			wantErr: true,
		},
		{
			name:    "git~1 8.3 short name",
			path:    "git~1/config",
			mode:    filemode.Regular,
			content: []byte("payload"),
			wantErr: true,
		},
		{
			name:    "NTFS trailing space on .git",
			path:    ".git /config",
			mode:    filemode.Regular,
			content: []byte("payload"),
			config:  map[string]string{"core.protectNTFS": "true"},
			wantErr: true,
		},
		{
			name:    "NTFS trailing dot on .git",
			path:    ".git./config",
			mode:    filemode.Regular,
			content: []byte("payload"),
			config:  map[string]string{"core.protectNTFS": "true"},
			wantErr: true,
		},
		{
			name:    "NTFS alternate data stream",
			path:    ".git::$INDEX_ALLOCATION/config",
			mode:    filemode.Regular,
			content: []byte("payload"),
			config:  map[string]string{"core.protectNTFS": "true"},
			wantErr: true,
		},
		{
			name:    "NTFS reserved device name CON",
			path:    "CON/file",
			mode:    filemode.Regular,
			content: []byte("payload"),
			config:  map[string]string{"core.protectNTFS": "true"},
			wantErr: true,
		},
		{
			name:    "NTFS reserved device name NUL",
			path:    "NUL",
			mode:    filemode.Regular,
			content: []byte("payload"),
			config:  map[string]string{"core.protectNTFS": "true"},
			wantErr: true,
		},
		{
			name:    "HFS+ zero-width character in .git",
			path:    ".g\u200cit/config",
			mode:    filemode.Regular,
			content: []byte("payload"),
			config:  map[string]string{"core.protectHFS": "true"},
			wantErr: true,
		},
		{
			name:    "symlink with absolute target",
			path:    "innocent",
			mode:    filemode.Symlink,
			content: []byte("/etc/passwd"),
			wantErr: true,
		},
		{
			name:    "symlink with .git target",
			path:    "innocent",
			mode:    filemode.Symlink,
			content: []byte(".git/config"),
			wantErr: true,
		},
		{
			name:    "symlink with parent-traversal target",
			path:    "innocent",
			mode:    filemode.Symlink,
			content: []byte("../escape"),
			wantErr: true,
		},
		{
			name:    "symlink named .gitmodules",
			path:    ".gitmodules",
			mode:    filemode.Symlink,
			content: []byte("payload"),
			wantErr: true,
		},
		{
			name:    "symlink named .gitmodules with NTFS trailing space",
			path:    ".gitmodules ",
			mode:    filemode.Symlink,
			content: []byte("payload"),
			config:  map[string]string{"core.protectNTFS": "true"},
			wantErr: true,
		},
		{
			name:    "symlink named gitmod~1",
			path:    "gitmod~1",
			mode:    filemode.Symlink,
			content: []byte("payload"),
			config:  map[string]string{"core.protectNTFS": "true"},
			wantErr: true,
		},
		{
			name:    "symlink with HFS-equivalent .gitmodules",
			path:    ".g\u200citmodules",
			mode:    filemode.Symlink,
			content: []byte("payload"),
			config:  map[string]string{"core.protectHFS": "true"},
			wantErr: true,
		},
	}

	runResetCases(t, tests)
}

// TestResetAcceptsLegitPaths verifies that legitimate Unicode paths
// and well-formed relative symlink targets pass validation, so that
// the conformance tests above don't silently turn into "rejects
// everything" via over-broad checks.
func TestResetAcceptsLegitPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping path validation conformance test in short mode")
	}
	t.Parallel()

	tests := []pathTestCase{
		{
			name:    "high-codepoint Unicode path",
			path:    "\u00c7ircle/file",
			mode:    filemode.Regular,
			content: []byte("legit"),
		},
		{
			name:    "ZWJ in non-dotgit name",
			path:    "ho\u200dme/note",
			mode:    filemode.Regular,
			content: []byte("legit"),
			config:  map[string]string{"core.protectHFS": "true"},
		},
		{
			name:    "relative symlink within worktree",
			path:    "link",
			mode:    filemode.Symlink,
			content: []byte("subdir/file"),
		},
	}

	runResetCases(t, tests)
}

// TestCheckoutRejectsDangerousTrees confirms that the same per-change
// validation applies to Checkout, not just Reset \u2014 both go through
// (*Worktree).validChange.
func TestCheckoutRejectsDangerousTrees(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping path validation conformance test in short mode")
	}
	t.Parallel()

	dir := t.TempDir()

	r, err := PlainInit(dir, false)
	require.NoError(t, err)

	w, err := r.Worktree()
	require.NoError(t, err)

	require.NoError(t, util.WriteFile(w.Filesystem, "README", []byte("init"), 0o644))
	_, err = w.Add("README")
	require.NoError(t, err)

	initHash, err := w.Commit("initial commit\n", &CommitOptions{Author: defaultSignature()})
	require.NoError(t, err)

	initCommit, err := r.CommitObject(initHash)
	require.NoError(t, err)

	badCommit := buildBadCommit(t, r.Storer, initCommit, initHash,
		".git/config", filemode.Regular, []byte("payload"))

	err = w.Checkout(&CheckoutOptions{Hash: badCommit.Hash, Force: true})
	assert.Error(t, err, "go-git should reject checkout onto a tree containing .git/config")
}

func runResetCases(t *testing.T, tests []pathTestCase) {
	t.Helper()

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			r, err := PlainInit(dir, false)
			require.NoError(t, err)

			w, err := r.Worktree()
			require.NoError(t, err)

			require.NoError(t, util.WriteFile(w.Filesystem, "README", []byte("init"), 0o644))
			_, err = w.Add("README")
			require.NoError(t, err)

			initHash, err := w.Commit("initial commit\n", &CommitOptions{Author: defaultSignature()})
			require.NoError(t, err)

			cfg, err := r.Config()
			require.NoError(t, err)
			for k, v := range tc.config {
				switch k {
				case "core.protectNTFS":
					cfg.Core.ProtectNTFS = config.NewOptBool(v == "true")
				case "core.protectHFS":
					cfg.Core.ProtectHFS = config.NewOptBool(v == "true")
				default:
					t.Fatalf("unsupported config override: %s", k)
				}
			}
			require.NoError(t, r.Storer.SetConfig(cfg))

			initCommit, err := r.CommitObject(initHash)
			require.NoError(t, err)

			badCommit := buildBadCommit(t, r.Storer, initCommit, initHash, tc.path, tc.mode, tc.content)

			err = w.Reset(&ResetOptions{Commit: badCommit.Hash, Mode: HardReset})
			if tc.wantErr {
				assert.Error(t, err, "go-git should reject reset onto %q", tc.path)
			} else {
				assert.NoError(t, err, "reset should accept %q", tc.path)
			}
		})
	}
}

func buildBadCommit(t *testing.T, s storer.Storer, parent *object.Commit, parentHash plumbing.Hash, filePath string, leafMode filemode.FileMode, content []byte) *object.Commit {
	t.Helper()

	blobObj := s.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(int64(len(content)))
	bw, err := blobObj.Writer()
	require.NoError(t, err)
	_, err = bw.Write(content)
	require.NoError(t, err)
	require.NoError(t, bw.Close())
	blobHash, err := s.SetEncodedObject(blobObj)
	require.NoError(t, err)

	parts := strings.Split(filePath, "/")
	leafHash := blobHash

	for i := len(parts) - 1; i >= 1; i-- {
		tree := &object.Tree{
			Entries: []object.TreeEntry{
				{Name: parts[i], Mode: leafMode, Hash: leafHash},
			},
		}
		treeObj := s.NewEncodedObject()
		require.NoError(t, tree.Encode(treeObj))
		leafHash, err = s.SetEncodedObject(treeObj)
		require.NoError(t, err)
		leafMode = filemode.Dir
	}

	parentTree, err := parent.Tree()
	require.NoError(t, err)

	entries := make([]object.TreeEntry, len(parentTree.Entries), len(parentTree.Entries)+1)
	copy(entries, parentTree.Entries)
	entries = append(entries, object.TreeEntry{
		Name: parts[0],
		Mode: leafMode,
		Hash: leafHash,
	})
	rootTree := &object.Tree{Entries: entries}
	sort.Sort(object.TreeEntrySorter(rootTree.Entries))
	rootObj := s.NewEncodedObject()
	require.NoError(t, rootTree.Encode(rootObj))
	rootHash, err := s.SetEncodedObject(rootObj)
	require.NoError(t, err)

	commit := &object.Commit{
		Author:       *defaultSignature(),
		Committer:    *defaultSignature(),
		Message:      "crafted: " + filePath + "\n",
		TreeHash:     rootHash,
		ParentHashes: []plumbing.Hash{parentHash},
	}
	commitObj := s.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := s.SetEncodedObject(commitObj)
	require.NoError(t, err)

	result, err := object.GetCommit(s, commitHash)
	require.NoError(t, err)
	return result
}
