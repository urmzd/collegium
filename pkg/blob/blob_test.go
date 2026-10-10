package blob_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urmzd/collegium/pkg/blob"
	"github.com/urmzd/collegium/pkg/blob/blobtest"
)

func TestMemory(t *testing.T) {
	blobtest.Run(t, func(*testing.T) blob.Store { return blob.NewMemory() })
}

func TestDisk(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store {
		d, err := blob.NewDisk(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	})
}

// A symbolic link inside the store must not lead out of it.
func TestDiskDoesNotFollowLinksOut(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := blob.NewDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	ctx := context.Background()
	if _, err := d.Put(ctx, "acme", "real", strings.NewReader("x"), 10); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "acme", "link")); err != nil {
		t.Skip("symbolic links are not available here")
	}
	if err := os.Symlink(outside, filepath.Join(dir, "acme", "dir")); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"link", "dir/secret"} {
		if _, _, err := d.Get(ctx, "acme", key); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("reading through %q = %v, want not found", key, err)
		}
	}
	if _, err := d.Put(ctx, "acme", "dir/planted", strings.NewReader("x"), 10); err == nil {
		t.Error("a write went through a link out of the store")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted")); err == nil {
		t.Error("a file was planted outside the store")
	}
	if b, _ := os.ReadFile(secret); string(b) != "not yours" {
		t.Error("the file outside the store was changed")
	}
	list, _ := d.List(ctx, "acme", "")
	if len(list) != 1 || list[0].Key != "real" {
		t.Errorf("list follows links: %+v", list)
	}
}
