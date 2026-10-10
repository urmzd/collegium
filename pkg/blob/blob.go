// Package blob stores the bytes of published files.
//
// A Store keeps opaque bytes under a tenant and a key. It knows nothing about
// titles, versions or who may read what: that is the artifact index's job.
// Keeping the interface this small is what lets a disk, a bucket or memory
// stand behind it, and blobtest holds the behaviour every one of them owes.
//
// A tenant is part of every call rather than part of the key, so that no
// caller can reach another tenant's bytes by choosing a key.
package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Errors a caller can match.
var (
	ErrNotFound = errors.New("blob: not found")
	ErrInvalid  = errors.New("blob: invalid")
	ErrTooLarge = errors.New("blob: too large")
)

// Info describes stored bytes.
type Info struct {
	Key    string    `json:"key"`
	Size   int64     `json:"size"`
	SHA256 string    `json:"sha256"`
	At     time.Time `json:"at"`
}

// Store keeps bytes. Every implementation must pass blobtest.Run.
type Store interface {
	// Put stores what r yields under a key, replacing what was there. It
	// reads at most max bytes and fails with ErrTooLarge, storing nothing,
	// if there is more. The returned Info carries the size and hash of what
	// was stored, computed by the store and not taken from the caller.
	Put(ctx context.Context, tenant, key string, r io.Reader, max int64) (Info, error)
	// Get opens stored bytes. The caller closes them.
	Get(ctx context.Context, tenant, key string) (io.ReadCloser, Info, error)
	Stat(ctx context.Context, tenant, key string) (Info, error)
	// Delete removes stored bytes. Deleting what is not there is ErrNotFound.
	Delete(ctx context.Context, tenant, key string) error
	// List returns what a tenant holds under a key prefix, ordered by key.
	List(ctx context.Context, tenant, prefix string) ([]Info, error)
}

// check refuses a tenant or key that could address anything but itself. Keys
// are made by the platform, never by a person or a model, so this is a guard
// against a mistake rather than the only defence.
func check(tenant, key string) error {
	for _, part := range []string{tenant, key} {
		if part == "" || len(part) > 512 || strings.ContainsAny(part, "\x00\\") || strings.HasPrefix(part, "/") {
			return fmt.Errorf("%w: %q", ErrInvalid, part)
		}
		for _, seg := range strings.Split(part, "/") {
			if seg == "" || seg == "." || seg == ".." {
				return fmt.Errorf("%w: %q", ErrInvalid, part)
			}
		}
	}
	if strings.Contains(tenant, "/") {
		return fmt.Errorf("%w: tenant %q", ErrInvalid, tenant)
	}
	return nil
}

// read takes at most max bytes from r, and reports ErrTooLarge if more remain.
func read(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		return nil, fmt.Errorf("%w: a size limit is required", ErrInvalid)
	}
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, max)
	}
	return b, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Memory keeps bytes in the process. It is for tests and for a server that is
// not meant to keep anything.
type Memory struct {
	mu   sync.RWMutex
	data map[string]entry
	now  func() time.Time
}

type entry struct {
	info Info
	b    []byte
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{data: map[string]entry{}, now: time.Now} }

func memKey(tenant, key string) string { return tenant + "\x00" + key }

// Put implements Store.
func (m *Memory) Put(_ context.Context, tenant, key string, r io.Reader, max int64) (Info, error) {
	if err := check(tenant, key); err != nil {
		return Info{}, err
	}
	b, err := read(r, max)
	if err != nil {
		return Info{}, err
	}
	info := Info{Key: key, Size: int64(len(b)), SHA256: sum(b), At: m.now()}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[memKey(tenant, key)] = entry{info: info, b: b}
	return info, nil
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, tenant, key string) (io.ReadCloser, Info, error) {
	if err := check(tenant, key); err != nil {
		return nil, Info{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.data[memKey(tenant, key)]
	if !ok {
		return nil, Info{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return io.NopCloser(bytes.NewReader(e.b)), e.info, nil
}

// Stat implements Store.
func (m *Memory) Stat(_ context.Context, tenant, key string) (Info, error) {
	if err := check(tenant, key); err != nil {
		return Info{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.data[memKey(tenant, key)]
	if !ok {
		return Info{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return e.info, nil
}

// Delete implements Store.
func (m *Memory) Delete(_ context.Context, tenant, key string) error {
	if err := check(tenant, key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[memKey(tenant, key)]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	delete(m.data, memKey(tenant, key))
	return nil
}

// List implements Store.
func (m *Memory) List(_ context.Context, tenant, prefix string) ([]Info, error) {
	if err := check(tenant, "x"); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Info
	for k, e := range m.data {
		if key, ok := strings.CutPrefix(k, tenant+"\x00"); ok && strings.HasPrefix(key, prefix) {
			out = append(out, e.info)
		}
	}
	slices.SortFunc(out, func(a, b Info) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// Disk keeps bytes as files under one directory, a directory per tenant.
//
// Every path is opened through an os.Root, so a key cannot name a file outside
// the store whatever it contains: not with "..", and not through a symbolic
// link someone put there.
type Disk struct {
	root *os.Root
	// mu makes a write and the read of what it replaces one step.
	mu sync.RWMutex
}

// NewDisk returns a store under dir, creating it if needed.
func NewDisk(dir string) (*Disk, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("blob: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("blob: %w", err)
	}
	return &Disk{root: root}, nil
}

// Close releases the store's directory.
func (d *Disk) Close() error { return d.root.Close() }

func diskPath(tenant, key string) string { return filepath.Join(tenant, filepath.FromSlash(key)) }

// Put implements Store.
func (d *Disk) Put(_ context.Context, tenant, key string, r io.Reader, max int64) (Info, error) {
	if err := check(tenant, key); err != nil {
		return Info{}, err
	}
	b, err := read(r, max)
	if err != nil {
		return Info{}, err
	}
	p := diskPath(tenant, key)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.root.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return Info{}, fmt.Errorf("blob: %w", err)
	}
	// Written beside the key and renamed over it, so a reader sees the old
	// bytes or the new ones and never half of either.
	tmp := p + ".part"
	if err := d.root.WriteFile(tmp, b, 0o600); err != nil {
		return Info{}, fmt.Errorf("blob: %w", err)
	}
	if err := d.root.Rename(tmp, p); err != nil {
		_ = d.root.Remove(tmp)
		return Info{}, fmt.Errorf("blob: %w", err)
	}
	st, err := d.root.Stat(p)
	if err != nil {
		return Info{}, fmt.Errorf("blob: %w", err)
	}
	return Info{Key: key, Size: int64(len(b)), SHA256: sum(b), At: st.ModTime()}, nil
}

func (d *Disk) load(tenant, key string) ([]byte, Info, error) {
	if err := check(tenant, key); err != nil {
		return nil, Info{}, err
	}
	p := diskPath(tenant, key)
	d.mu.RLock()
	defer d.mu.RUnlock()
	st, err := d.root.Lstat(p)
	if err != nil || !st.Mode().IsRegular() {
		return nil, Info{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	b, err := d.root.ReadFile(p)
	if err != nil {
		return nil, Info{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return b, Info{Key: key, Size: int64(len(b)), SHA256: sum(b), At: st.ModTime()}, nil
}

// Get implements Store.
func (d *Disk) Get(_ context.Context, tenant, key string) (io.ReadCloser, Info, error) {
	b, info, err := d.load(tenant, key)
	if err != nil {
		return nil, Info{}, err
	}
	return io.NopCloser(bytes.NewReader(b)), info, nil
}

// Stat implements Store.
func (d *Disk) Stat(_ context.Context, tenant, key string) (Info, error) {
	_, info, err := d.load(tenant, key)
	return info, err
}

// Delete implements Store.
func (d *Disk) Delete(_ context.Context, tenant, key string) error {
	if err := check(tenant, key); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.root.Remove(diskPath(tenant, key)); err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return nil
}

// List implements Store.
func (d *Disk) List(ctx context.Context, tenant, prefix string) ([]Info, error) {
	if err := check(tenant, "x"); err != nil {
		return nil, err
	}
	var keys []string
	d.mu.RLock()
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		f, err := d.root.Open(dir)
		if err != nil {
			return
		}
		entries, _ := f.ReadDir(-1)
		_ = f.Close()
		for _, e := range entries {
			key := e.Name()
			if rel != "" {
				key = rel + "/" + e.Name()
			}
			switch {
			case e.IsDir():
				walk(filepath.Join(dir, e.Name()), key)
			case e.Type().IsRegular() && !strings.HasSuffix(key, ".part") && strings.HasPrefix(key, prefix):
				keys = append(keys, key)
			}
		}
	}
	walk(tenant, "")
	d.mu.RUnlock()
	slices.Sort(keys)
	out := make([]Info, 0, len(keys))
	for _, k := range keys {
		if info, err := d.Stat(ctx, tenant, k); err == nil {
			out = append(out, info)
		}
	}
	return out, nil
}
