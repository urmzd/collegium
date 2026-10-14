package clients

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// Caps is how many clients a workspace may have. Zero means none: a caller
// that wants the defaults says so.
type Caps struct {
	// Max is the most clients in the workspace.
	Max int `json:"max"`
	// MaxPerSurface is the most on any one surface.
	MaxPerSurface int `json:"max_per_surface"`
}

// DefaultCaps are the caps of a workspace whose policy sets none.
func DefaultCaps() Caps { return Caps{Max: DefaultMax, MaxPerSurface: DefaultMaxPerSurface} }

// Store keeps client records. Every read and write names a tenant, and a
// client in another tenant is reported as ErrNotFound.
//
// A durable store goes behind these six methods: one table keyed by tenant and
// id, with Create's count taken in the same transaction as its insert.
type Store interface {
	// Create adds a client if the caps leave room. The count and the insert
	// are one step, so two creates cannot both take the last place.
	Create(tenant string, c Client, caps Caps, at time.Time) (Client, error)
	// Update replaces a client's record, keeping its id, surface and origin.
	Update(tenant string, c Client, at time.Time) (Client, error)
	Get(tenant, id string) (Client, error)
	// List returns a tenant's clients, oldest first, never more than Ceiling.
	List(tenant string) []Client
	Delete(tenant, id string) error
	// On returns every enabled client on a surface, in every tenant, oldest
	// first. It exists for ingress, which has no tenant until a client's
	// secret has verified the request.
	On(surface Surface) []Client
}

// Memory is a Store in memory. It is lost on restart.
type Memory struct {
	mu sync.RWMutex
	// all is every client, oldest first.
	all []Client
}

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{} }

var _ Store = (*Memory)(nil)

func (m *Memory) index(tenant, id string) int {
	return slices.IndexFunc(m.all, func(c Client) bool { return c.Tenant == tenant && c.ID == id })
}

// Create implements Store.
func (m *Memory) Create(tenant string, c Client, caps Caps, at time.Time) (Client, error) {
	c = c.clone()
	c.Tenant = tenant
	if tenant == "" {
		return Client{}, fmt.Errorf("%w: a client belongs to a workspace", ErrInvalid)
	}
	if err := c.Validate(); err != nil {
		return Client{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index(tenant, c.ID) >= 0 {
		return Client{}, fmt.Errorf("%w: %q", ErrExists, c.ID)
	}
	total, onSurface := 0, 0
	for _, other := range m.all {
		if other.Tenant != tenant {
			continue
		}
		total++
		if other.Surface == c.Surface {
			onSurface++
			if other.overlaps(c) {
				return Client{}, fmt.Errorf("%w: %q already answers for that %s identity", ErrExists, other.ID, c.Surface)
			}
		}
	}
	switch {
	case total >= min(caps.Max, Ceiling):
		return Client{}, fmt.Errorf("%w: the workspace may have %d clients", ErrLimit, min(caps.Max, Ceiling))
	case onSurface >= caps.MaxPerSurface:
		return Client{}, fmt.Errorf("%w: the workspace may have %d %s clients", ErrLimit, caps.MaxPerSurface, c.Surface)
	}
	c.Version, c.CreatedAt, c.UpdatedAt = 1, at, at
	m.all = append(m.all, c)
	return c.clone(), nil
}

// Update implements Store.
func (m *Memory) Update(tenant string, c Client, at time.Time) (Client, error) {
	c = c.clone()
	c.Tenant = tenant
	if err := c.Validate(); err != nil {
		return Client{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.index(tenant, c.ID)
	if i < 0 {
		return Client{}, fmt.Errorf("%w: %q", ErrNotFound, c.ID)
	}
	old := m.all[i]
	if old.Surface != c.Surface {
		return Client{}, fmt.Errorf("%w: a client stays on the surface it was created on", ErrInvalid)
	}
	for _, other := range m.all {
		if other.Tenant == tenant && other.ID != c.ID && other.overlaps(c) {
			return Client{}, fmt.Errorf("%w: %q already answers for that %s identity", ErrExists, other.ID, c.Surface)
		}
	}
	c.Managed, c.CreatedBy, c.CreatedAt = old.Managed, old.CreatedBy, old.CreatedAt
	c.Version, c.UpdatedAt = old.Version+1, at
	m.all[i] = c
	return c.clone(), nil
}

// Get implements Store.
func (m *Memory) Get(tenant, id string) (Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	i := m.index(tenant, id)
	if i < 0 {
		return Client{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return m.all[i].clone(), nil
}

// List implements Store.
func (m *Memory) List(tenant string) []Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Client{}
	for _, c := range m.all {
		if c.Tenant == tenant && len(out) < Ceiling {
			out = append(out, c.clone())
		}
	}
	return out
}

// Delete implements Store.
func (m *Memory) Delete(tenant, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.index(tenant, id)
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	m.all = slices.Delete(m.all, i, i+1)
	return nil
}

// On implements Store.
func (m *Memory) On(surface Surface) []Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Client
	for _, c := range m.all {
		if c.Surface == surface && c.Enabled {
			out = append(out, c.clone())
		}
	}
	return out
}

// Secrets keeps the secret values written for clients, apart from the records.
// A durable deployment puts a secret manager behind it.
type Secrets interface {
	Put(tenant, client, name, value string)
	Get(tenant, client, name string) (string, bool)
	// Drop forgets every secret of a client.
	Drop(tenant, client string)
}

// MemorySecrets is Secrets in memory. It is lost on restart, so a client whose
// secrets were written as values has to be given them again.
type MemorySecrets struct {
	mu     sync.RWMutex
	values map[[3]string]string
}

// NewMemorySecrets returns an empty secret store.
func NewMemorySecrets() *MemorySecrets { return &MemorySecrets{values: map[[3]string]string{}} }

var _ Secrets = (*MemorySecrets)(nil)

// Put implements Secrets.
func (s *MemorySecrets) Put(tenant, client, name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[[3]string{tenant, client, name}] = value
}

// Get implements Secrets.
func (s *MemorySecrets) Get(tenant, client, name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[[3]string{tenant, client, name}]
	return v, ok
}

// Drop implements Secrets.
func (s *MemorySecrets) Drop(tenant, client string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.values {
		if k[0] == tenant && k[1] == client {
			delete(s.values, k)
		}
	}
}
