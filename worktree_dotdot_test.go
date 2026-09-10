package git

import (
	"fmt"
	gofs "io/fs"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"
)

// recordingFS records every path handed to a mutating operation, so a
// test can assert on which strings actually reach the filesystem
// after the wrapper's validators have had their say. Asserting on the
// returned error is not enough: a delete that succeeds and a delete
// that is refused can both leave Reset returning nil.
type recordingFS struct {
	billy.Filesystem
	calls []string
}

func (r *recordingFS) record(op, p string) { r.calls = append(r.calls, op+" "+p) }

func (r *recordingFS) Remove(p string) error {
	r.record("Remove", p)
	return r.Filesystem.Remove(p)
}

func (r *recordingFS) OpenFile(p string, flag int, perm gofs.FileMode) (billy.File, error) {
	r.record("OpenFile", p)
	return r.Filesystem.OpenFile(p, flag, perm)
}

func (r *recordingFS) MkdirAll(p string, perm gofs.FileMode) error {
	r.record("MkdirAll", p)
	return r.Filesystem.MkdirAll(p, perm)
}

func (r *recordingFS) sawPath(name string) bool {
	for _, c := range r.calls {
		if strings.HasSuffix(c, " "+name) {
			return true
		}
	}
	return false
}

// writeRawObject stores an object with an attacker-chosen payload,
// bypassing object.Tree.Encode, which runs Tree.Validate and refuses
// a hostile entry name. A pack from a hostile remote arrives through
// the permissive Decode path, so raw bytes are the faithful model of
// what a repository can actually contain.
func writeRawObject(t *testing.T, s *memory.Storage, typ plumbing.ObjectType, payload []byte) plumbing.Hash {
	t.Helper()

	o := s.NewEncodedObject()
	o.SetType(typ)
	w, err := o.Writer()
	require.NoError(t, err)
	_, err = w.Write(payload)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	h, err := s.SetEncodedObject(o)
	require.NoError(t, err)
	return h
}

func encodeObject(t *testing.T, s *memory.Storage, e interface {
	Encode(plumbing.EncodedObject) error
}) plumbing.Hash {
	t.Helper()

	o := s.NewEncodedObject()
	require.NoError(t, e.Encode(o))
	h, err := s.SetEncodedObject(o)
	require.NoError(t, err)
	return h
}

// TestResetHardRefusesTreeDerivedDotDotDisguise closes the delete
// half of checkout and reset. resetWorktreeToTree's first pass takes
// ch.From.String() straight from diffTrees into rmFileAndDirsIfEmpty,
// and diffTrees' treeNoder sets TreeWalker.skipPathValidation, so the
// name never meets pathutil.ValidTreePath. The wrapper's validPath is
// the only gate, and it must hold with both protections off as well
// as on.
//
// Reachability does not require the hostile tree ever to have been
// materialised: Reset sets HEAD before resetIndex, so an aborted
// checkout leaves HEAD on the hostile commit and the next
// reset --hard issues the delete. The test models exactly that by
// pointing HEAD at the hostile commit directly.
func TestResetHardRefusesTreeDerivedDotDotDisguise(t *testing.T) {
	t.Parallel()

	const hostile = ".. "

	for _, tc := range []struct {
		name        string
		protectNTFS config.OptBool
		protectHFS  config.OptBool
	}{
		{name: "repository defaults"},
		{
			name:        "protections off",
			protectNTFS: config.NewOptBool(false),
			protectHFS:  config.NewOptBool(false),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := memory.NewStorage()
			rec := &recordingFS{Filesystem: memfs.New()}

			r, err := Init(s, WithWorkTree(rec))
			require.NoError(t, err)

			if tc.protectNTFS.IsSet() || tc.protectHFS.IsSet() {
				cfg, err := r.Config()
				require.NoError(t, err)
				cfg.Core.ProtectNTFS = tc.protectNTFS
				cfg.Core.ProtectHFS = tc.protectHFS
				require.NoError(t, r.SetConfig(cfg))
			}

			blob := writeRawObject(t, s, plumbing.BlobObject, []byte("payload\n"))

			// Hostile tree: one regular-file entry named ".. ".
			var payload []byte
			payload = append(payload, fmt.Sprintf("%o %s", 0o100644, hostile)...)
			payload = append(payload, 0x00)
			payload = append(payload, blob.Bytes()...)
			hostileTree := writeRawObject(t, s, plumbing.TreeObject, payload)

			benignTree := writeRawObject(t, s, plumbing.TreeObject, nil)

			sig := object.Signature{
				Name:  "a",
				Email: "a@b.c",
				When:  time.Now(),
			}
			hostileCommit := encodeObject(t, s, &object.Commit{
				Author:    sig,
				Committer: sig,
				Message:   "hostile\n",
				TreeHash:  hostileTree,
			})
			benignCommit := encodeObject(t, s, &object.Commit{
				Author:       sig,
				Committer:    sig,
				Message:      "benign\n",
				TreeHash:     benignTree,
				ParentHashes: []plumbing.Hash{hostileCommit},
			})

			// The hostile name really is in the stored tree, and the
			// tree-path validator really does refuse it.
			tr, err := object.GetTree(s, hostileTree)
			require.NoError(t, err)
			require.Len(t, tr.Entries, 1)
			require.Equal(t, hostile, tr.Entries[0].Name)
			_, err = tr.FindEntry(hostile)
			require.Error(t, err, "FindEntry must refuse the disguise")

			head, err := r.Reference(plumbing.HEAD, false)
			require.NoError(t, err)
			require.NoError(t, s.SetReference(
				plumbing.NewHashReference(head.Target(), hostileCommit)))

			w, err := r.Worktree()
			require.NoError(t, err)

			err = w.Reset(&ResetOptions{Mode: HardReset, Commit: benignCommit})
			t.Logf("Reset returned: %v", err)
			t.Logf("filesystem calls: %q", rec.calls)

			assert.False(t, rec.sawPath(hostile),
				"the disguise %q must never reach the filesystem; calls=%q",
				hostile, rec.calls)
		})
	}
}
