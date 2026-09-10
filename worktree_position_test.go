package git

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"

	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"
)

func TestResetRejectsDotGitPositionShift(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"::$INDEX_ALLOCATION/.git", ":a/.git", ".\u200c/.git", ". /.git", ".../.git", " /.git", "sub/.GIT", ".git"} {
		for _, mode := range []int{0o100644, 0o160000} {
			for _, ntfs := range []bool{false, true} {
				for _, hfs := range []bool{false, true} {
					t.Run(fmt.Sprintf("%q/%o/%t/%t", name, mode, ntfs, hfs), func(t *testing.T) {
						t.Parallel()
						s := memory.NewStorage()
						rec := &recordingFS{Filesystem: memfs.New()}
						r, e := Init(s, WithWorkTree(rec))
						if e != nil {
							t.Fatal(e)
						}
						cfg, e := r.Config()
						if e != nil {
							t.Fatal(e)
						}
						cfg.Core.ProtectNTFS = config.NewOptBool(ntfs)
						cfg.Core.ProtectHFS = config.NewOptBool(hfs)
						if e = r.SetConfig(cfg); e != nil {
							t.Fatal(e)
						}
						blob := writeRawObject(t, s, plumbing.BlobObject, []byte("payload"))
						payload := append(fmt.Appendf(nil, "%o %s\x00", mode, name), blob.Bytes()...)
						hostileTree := writeRawObject(t, s, plumbing.TreeObject, payload)
						benignTree := writeRawObject(t, s, plumbing.TreeObject, nil)
						sig := object.Signature{Name: "Test", Email: "test@example.invalid", When: time.Unix(0, 0)}
						bad := encodeObject(t, s, &object.Commit{Author: sig, Committer: sig, Message: "bad", TreeHash: hostileTree})
						good := encodeObject(t, s, &object.Commit{Author: sig, Committer: sig, Message: "good", TreeHash: benignTree, ParentHashes: []plumbing.Hash{bad}})
						head, e := r.Reference(plumbing.HEAD, false)
						if e != nil {
							t.Fatal(e)
						}
						if e = s.SetReference(plumbing.NewHashReference(head.Target(), bad)); e != nil {
							t.Fatal(e)
						}
						w, e := r.Worktree()
						if e != nil {
							t.Fatal(e)
						}
						rec.calls = nil
						e = w.Reset(&ResetOptions{Mode: HardReset, Commit: good})
						for _, call := range rec.calls {
							if call == "Remove "+name || call == "OpenFile "+name {
								t.Errorf("hostile path reached filesystem: %q", call)
							}
						}
						if e == nil {
							t.Error("security refusal not returned")
						}
					})
				}
			}
		}
	}
}
