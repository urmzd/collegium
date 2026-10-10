// Package prefs holds what one person has chosen for themselves: how they are
// shown and how the console looks to them.
//
// Preferences belong to a person in a tenant. Nobody else reads or writes them,
// and they change nothing about what the person is allowed to do.
//
// The store is in memory and is lost on restart.
package prefs

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Errors.
var (
	ErrInvalid = errors.New("prefs: invalid")
	ErrNoPhoto = errors.New("prefs: no photo")
)

// MaxPhoto bounds an uploaded profile picture, in bytes.
const MaxPhoto = 512 << 10

// maxName bounds a display name, in bytes.
const maxName = 80

// Theme is how the console is drawn.
type Theme string

const (
	// System follows the device's setting.
	System Theme = "system"
	Light  Theme = "light"
	Dark   Theme = "dark"
)

// Accents are the accent colors the console offers.
var Accents = []string{"purple", "blue", "yellow", "teal"}

// Preferences is one person's choices.
type Preferences struct {
	// DisplayName is shown in place of the name the identity provider gave.
	DisplayName string `json:"display_name"`
	Theme       Theme  `json:"theme"`
	Accent      string `json:"accent"`
	// HasPhoto reports whether a profile picture is stored.
	HasPhoto bool `json:"has_photo"`
	// PhotoVersion changes with every upload, so a browser never shows a
	// stale picture from its cache.
	PhotoVersion int `json:"photo_version"`
}

// Defaults is what a person has before they choose anything.
func Defaults() Preferences { return Preferences{Theme: System, Accent: Accents[0]} }

type photo struct {
	data []byte
	mime string
}

type entry struct {
	Preferences
	photo photo
}

// Store holds every person's preferences.
type Store struct {
	mu sync.RWMutex
	by map[string]*entry
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{by: map[string]*entry{}} }

func key(tenant, subject string) string { return tenant + "\x00" + subject }

func (s *Store) of(tenant, subject string) *entry {
	e, ok := s.by[key(tenant, subject)]
	if !ok {
		e = &entry{Preferences: Defaults()}
		s.by[key(tenant, subject)] = e
	}
	return e
}

// Get returns a person's preferences.
func (s *Store) Get(tenant, subject string) Preferences {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e, ok := s.by[key(tenant, subject)]; ok {
		return e.Preferences
	}
	return Defaults()
}

// Set replaces what a person can choose. The photo is changed separately.
func (s *Store) Set(tenant, subject string, next Preferences) (Preferences, error) {
	next.DisplayName = strings.TrimSpace(next.DisplayName)
	switch {
	case len(next.DisplayName) > maxName:
		return Preferences{}, fmt.Errorf("%w: a display name is at most %d characters", ErrInvalid, maxName)
	case strings.ContainsAny(next.DisplayName, "\r\n\t<>"):
		return Preferences{}, fmt.Errorf("%w: a display name is plain text on one line", ErrInvalid)
	case next.Theme != System && next.Theme != Light && next.Theme != Dark:
		return Preferences{}, fmt.Errorf("%w: theme is system, light or dark", ErrInvalid)
	}
	ok := false
	for _, a := range Accents {
		ok = ok || a == next.Accent
	}
	if !ok {
		return Preferences{}, fmt.Errorf("%w: accent is one of %s", ErrInvalid, strings.Join(Accents, ", "))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.of(tenant, subject)
	e.DisplayName, e.Theme, e.Accent = next.DisplayName, next.Theme, next.Accent
	return e.Preferences, nil
}

// imageType reports the type of an image from its own bytes. What the uploader
// says the file is plays no part: only a real PNG, JPEG or WebP is accepted,
// which keeps out SVG and HTML, both of which can carry script.
func imageType(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", true
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "image/jpeg", true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", true
	}
	return "", false
}

// SetPhoto stores a profile picture.
func (s *Store) SetPhoto(tenant, subject string, data []byte) (Preferences, error) {
	if len(data) == 0 {
		return Preferences{}, fmt.Errorf("%w: the picture is empty", ErrInvalid)
	}
	if len(data) > MaxPhoto {
		return Preferences{}, fmt.Errorf("%w: a picture is at most %d KB", ErrInvalid, MaxPhoto>>10)
	}
	mime, ok := imageType(data)
	// The standard sniffer must agree, as a second opinion on the bytes.
	if !ok || http.DetectContentType(data) != mime {
		return Preferences{}, fmt.Errorf("%w: a picture is a PNG, JPEG or WebP image", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.of(tenant, subject)
	e.photo = photo{data: bytes.Clone(data), mime: mime}
	e.HasPhoto = true
	e.PhotoVersion++
	return e.Preferences, nil
}

// ClearPhoto removes a profile picture.
func (s *Store) ClearPhoto(tenant, subject string) Preferences {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.of(tenant, subject)
	e.photo, e.HasPhoto = photo{}, false
	e.PhotoVersion++
	return e.Preferences
}

// Photo returns a person's picture and its type.
func (s *Store) Photo(tenant, subject string) ([]byte, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.by[key(tenant, subject)]
	if !ok || !e.HasPhoto {
		return nil, "", ErrNoPhoto
	}
	return e.photo.data, e.photo.mime, nil
}
