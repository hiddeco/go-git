package git

import (
	"io"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"
)

func TestCheckoutMaterialisesPOSIXTrailingNames(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX contract")
	}
	paths := []string{"trail.", "trail ", ".../inner.txt", ". /inner.txt", ".\u200c/inner.txt", "aux.c", "a:b.txt", ".gitattributes ", "gi7eba~1"}
	fs := memfs.New()
	st := memory.NewStorage()
	r, err := Init(st, WithWorkTree(fs))
	if err != nil {
		t.Fatal(err)
	}
	save := func(typ plumbing.ObjectType, data string) plumbing.Hash {
		obj := st.NewEncodedObject()
		obj.SetType(typ)
		w, e := obj.Writer()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = io.WriteString(w, data); e != nil {
			t.Fatal(e)
		}
		if e = w.Close(); e != nil {
			t.Fatal(e)
		}
		h, e := st.SetEncodedObject(obj)
		if e != nil {
			t.Fatal(e)
		}
		return h
	}
	blob := save(plumbing.BlobObject, "payload")
	saveTree := func(entries []object.TreeEntry) plumbing.Hash {
		key := func(e object.TreeEntry) string {
			if e.Mode == filemode.Dir {
				return e.Name + "/"
			}
			return e.Name
		}
		sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })
		tree := object.Tree{Entries: entries}
		obj := st.NewEncodedObject()
		if e := tree.Encode(obj); e != nil {
			t.Fatal(e)
		}
		h, e := st.SetEncodedObject(obj)
		if e != nil {
			t.Fatal(e)
		}
		return h
	}
	var entries []object.TreeEntry
	for _, p := range paths {
		parts := strings.Split(p, "/")
		if len(parts) == 1 {
			entries = append(entries, object.TreeEntry{Name: p, Mode: filemode.Regular, Hash: blob})
			continue
		}
		child := saveTree([]object.TreeEntry{{Name: parts[1], Mode: filemode.Regular, Hash: blob}})
		entries = append(entries, object.TreeEntry{Name: parts[0], Mode: filemode.Dir, Hash: child})
	}
	tree := saveTree(entries)
	sig := object.Signature{Name: "Test", Email: "test@example.invalid", When: time.Unix(0, 0)}
	commit := object.Commit{Author: sig, Committer: sig, TreeHash: tree, Message: "fixture"}
	obj := st.NewEncodedObject()
	if err = commit.Encode(obj); err != nil {
		t.Fatal(err)
	}
	hash, err := st.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Checkout(&CheckoutOptions{Hash: hash, Force: true}); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		f, e := fs.Open(p)
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(f)
		f.Close()
		if e != nil || string(b) != "payload" {
			t.Fatalf("%q: %q %v", p, b, e)
		}
	}
}
