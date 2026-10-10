// Package blobtest holds what every blob.Store owes its callers. A new
// backend is correct when Run passes against it.
package blobtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/urmzd/open-agent-platform/pkg/blob"
)

func body(t *testing.T, s blob.Store, tenant, key string) string {
	t.Helper()
	r, _, err := s.Get(context.Background(), tenant, key)
	if err != nil {
		t.Fatalf("get %s/%s: %v", tenant, key, err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Run checks a store. open must return a new, empty store each time.
func Run(t *testing.T, open func(t *testing.T) blob.Store) {
	ctx := context.Background()
	const max = 1 << 20

	t.Run("what is put is what is got", func(t *testing.T) {
		s := open(t)
		content := "a,b\n1,2\n"
		info, err := s.Put(ctx, "acme", "a/b/report.csv", strings.NewReader(content), max)
		if err != nil {
			t.Fatal(err)
		}
		want := sha256.Sum256([]byte(content))
		if info.Key != "a/b/report.csv" || info.Size != int64(len(content)) || info.SHA256 != hex.EncodeToString(want[:]) || info.At.IsZero() {
			t.Fatalf("info = %+v", info)
		}
		if got := body(t, s, "acme", "a/b/report.csv"); got != content {
			t.Fatalf("got %q", got)
		}
		st, err := s.Stat(ctx, "acme", "a/b/report.csv")
		if err != nil || st.Size != info.Size || st.SHA256 != info.SHA256 {
			t.Fatalf("stat = %+v, %v", st, err)
		}
	})

	t.Run("empty and binary content survive", func(t *testing.T) {
		s := open(t)
		if info, err := s.Put(ctx, "acme", "empty", strings.NewReader(""), max); err != nil || info.Size != 0 {
			t.Fatalf("empty = %+v, %v", info, err)
		}
		bin := []byte{0, 1, 2, 0xff, 0, '\n', 0xfe}
		if _, err := s.Put(ctx, "acme", "bin", bytes.NewReader(bin), max); err != nil {
			t.Fatal(err)
		}
		if got := body(t, s, "acme", "bin"); got != string(bin) {
			t.Fatalf("binary came back as %q", got)
		}
	})

	t.Run("a put replaces", func(t *testing.T) {
		s := open(t)
		_, _ = s.Put(ctx, "acme", "k", strings.NewReader("one"), max)
		info, err := s.Put(ctx, "acme", "k", strings.NewReader("second"), max)
		if err != nil || info.Size != 6 {
			t.Fatalf("replace = %+v, %v", info, err)
		}
		if got := body(t, s, "acme", "k"); got != "second" {
			t.Fatalf("got %q", got)
		}
		if list, _ := s.List(ctx, "acme", ""); len(list) != 1 {
			t.Fatalf("replacing left %d entries", len(list))
		}
	})

	t.Run("too large stores nothing", func(t *testing.T) {
		s := open(t)
		_, _ = s.Put(ctx, "acme", "k", strings.NewReader("kept"), max)
		if _, err := s.Put(ctx, "acme", "k", strings.NewReader("123456"), 5); !errors.Is(err, blob.ErrTooLarge) {
			t.Fatalf("over the limit = %v", err)
		}
		if got := body(t, s, "acme", "k"); got != "kept" {
			t.Fatalf("a refused put changed what was stored: %q", got)
		}
		if _, err := s.Put(ctx, "acme", "new", strings.NewReader("123456"), 5); !errors.Is(err, blob.ErrTooLarge) {
			t.Fatalf("over the limit = %v", err)
		}
		if _, err := s.Stat(ctx, "acme", "new"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("a refused put left something behind: %v", err)
		}
		if info, err := s.Put(ctx, "acme", "exact", strings.NewReader("12345"), 5); err != nil || info.Size != 5 {
			t.Fatalf("exactly the limit = %+v, %v", info, err)
		}
		if _, err := s.Put(ctx, "acme", "k2", strings.NewReader("x"), 0); !errors.Is(err, blob.ErrInvalid) {
			t.Fatalf("no limit = %v", err)
		}
	})

	t.Run("missing is not found", func(t *testing.T) {
		s := open(t)
		if _, _, err := s.Get(ctx, "acme", "nope"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("get = %v", err)
		}
		if _, err := s.Stat(ctx, "acme", "nope"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("stat = %v", err)
		}
		if err := s.Delete(ctx, "acme", "nope"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("delete = %v", err)
		}
	})

	t.Run("delete removes", func(t *testing.T) {
		s := open(t)
		_, _ = s.Put(ctx, "acme", "k", strings.NewReader("x"), max)
		if err := s.Delete(ctx, "acme", "k"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Stat(ctx, "acme", "k"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("after delete: %v", err)
		}
		if err := s.Delete(ctx, "acme", "k"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("a second delete = %v", err)
		}
	})

	t.Run("tenants cannot reach each other", func(t *testing.T) {
		s := open(t)
		_, _ = s.Put(ctx, "acme", "k", strings.NewReader("acme's"), max)
		_, _ = s.Put(ctx, "globex", "k", strings.NewReader("globex's"), max)
		if got := body(t, s, "acme", "k"); got != "acme's" {
			t.Fatalf("acme reads %q", got)
		}
		if err := s.Delete(ctx, "globex", "k"); err != nil {
			t.Fatal(err)
		}
		if got := body(t, s, "acme", "k"); got != "acme's" {
			t.Fatal("deleting in one tenant reached another")
		}
		if list, _ := s.List(ctx, "globex", ""); len(list) != 0 {
			t.Fatalf("globex lists %v", list)
		}
		// A key cannot name another tenant, or anything above its own.
		for _, key := range []string{"../acme/k", "../../etc/passwd", "/etc/passwd", "a/../../acme/k", "a//b", ".", "..", "a/./b", "", "a\x00b", `a\..\b`} {
			if _, err := s.Put(ctx, "globex", key, strings.NewReader("x"), max); !errors.Is(err, blob.ErrInvalid) {
				t.Errorf("put %q = %v, want invalid", key, err)
			}
			if _, _, err := s.Get(ctx, "globex", key); !errors.Is(err, blob.ErrInvalid) {
				t.Errorf("get %q = %v, want invalid", key, err)
			}
			if err := s.Delete(ctx, "globex", key); !errors.Is(err, blob.ErrInvalid) {
				t.Errorf("delete %q = %v, want invalid", key, err)
			}
		}
		for _, tenant := range []string{"", "..", "a/b", "../acme", "."} {
			if _, err := s.Put(ctx, tenant, "k", strings.NewReader("x"), max); !errors.Is(err, blob.ErrInvalid) {
				t.Errorf("tenant %q = %v, want invalid", tenant, err)
			}
			if _, err := s.List(ctx, tenant, ""); !errors.Is(err, blob.ErrInvalid) {
				t.Errorf("list in tenant %q = %v, want invalid", tenant, err)
			}
		}
		if got := body(t, s, "acme", "k"); got != "acme's" {
			t.Fatal("a refused key changed another tenant's bytes")
		}
	})

	t.Run("list is by prefix and in order", func(t *testing.T) {
		s := open(t)
		for _, k := range []string{"b/2", "a/1", "a/3", "a/2", "ab"} {
			_, _ = s.Put(ctx, "acme", k, strings.NewReader(k), max)
		}
		list, err := s.List(ctx, "acme", "a/")
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, i := range list {
			keys = append(keys, i.Key)
			if i.Size != 3 || i.SHA256 == "" {
				t.Fatalf("listed info = %+v", i)
			}
		}
		if strings.Join(keys, " ") != "a/1 a/2 a/3" {
			t.Fatalf("list a/ = %v", keys)
		}
		if all, _ := s.List(ctx, "acme", ""); len(all) != 5 {
			t.Fatalf("list everything = %d", len(all))
		}
		if none, err := s.List(ctx, "acme", "zzz"); err != nil || len(none) != 0 {
			t.Fatalf("list nothing = %v, %v", none, err)
		}
	})

	t.Run("writers do not corrupt each other", func(t *testing.T) {
		s := open(t)
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Go(func() {
				content := strings.Repeat(fmt.Sprint(i%10), 4096)
				if _, err := s.Put(ctx, "acme", "shared", strings.NewReader(content), max); err != nil {
					t.Error(err)
				}
				if _, err := s.Put(ctx, "acme", fmt.Sprint("own/", i), strings.NewReader(content), max); err != nil {
					t.Error(err)
				}
				_, _, _ = s.Get(ctx, "acme", "shared")
			})
		}
		wg.Wait()
		got := body(t, s, "acme", "shared")
		if len(got) != 4096 || strings.Trim(got, got[:1]) != "" {
			t.Fatalf("the shared key holds a mix of writes (%d bytes)", len(got))
		}
		info, _ := s.Stat(ctx, "acme", "shared")
		want := sha256.Sum256([]byte(got))
		if info.SHA256 != hex.EncodeToString(want[:]) {
			t.Fatal("the hash does not match the bytes")
		}
		if list, _ := s.List(ctx, "acme", "own/"); len(list) != 16 {
			t.Fatalf("%d of 16 writes landed", len(list))
		}
	})
}
