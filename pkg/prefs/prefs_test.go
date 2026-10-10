package prefs

import (
	"bytes"
	"errors"
	"testing"
)

// The smallest files that are really these types.
var (
	png  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	jpeg = append([]byte("\xff\xd8\xff\xe0"), make([]byte, 32)...)
	webp = append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
)

func TestDefaults(t *testing.T) {
	got := NewStore().Get("acme", "ada")
	if got.Theme != System || got.Accent != "purple" || got.HasPhoto {
		t.Fatalf("got %+v", got)
	}
}

func TestSet(t *testing.T) {
	s := NewStore()
	got, err := s.Set("acme", "ada", Preferences{DisplayName: "  Ada L.  ", Theme: Dark, Accent: "teal"})
	if err != nil || got.DisplayName != "Ada L." || got.Theme != Dark {
		t.Fatalf("got %+v, %v", got, err)
	}
	for name, p := range map[string]Preferences{
		"theme":     {Theme: "neon", Accent: "teal"},
		"accent":    {Theme: Dark, Accent: "url(evil)"},
		"markup":    {DisplayName: "<img src=x>", Theme: Dark, Accent: "teal"},
		"two lines": {DisplayName: "a\nb", Theme: Dark, Accent: "teal"},
		"too long":  {DisplayName: string(make([]byte, 200)), Theme: Dark, Accent: "teal"},
	} {
		if _, err := s.Set("acme", "ada", p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if got := s.Get("acme", "ada"); got.DisplayName != "Ada L." {
		t.Fatalf("a rejected change was stored: %+v", got)
	}
}

func TestPreferencesBelongToOnePerson(t *testing.T) {
	s := NewStore()
	_, _ = s.Set("acme", "ada", Preferences{DisplayName: "Ada", Theme: Dark, Accent: "teal"})
	_, _ = s.SetPhoto("acme", "ada", png)
	for _, who := range [][2]string{{"acme", "bob"}, {"globex", "ada"}} {
		if got := s.Get(who[0], who[1]); got.DisplayName != "" || got.HasPhoto {
			t.Errorf("%v sees %+v", who, got)
		}
		if _, _, err := s.Photo(who[0], who[1]); !errors.Is(err, ErrNoPhoto) {
			t.Errorf("%v read the photo: %v", who, err)
		}
	}
}

func TestPhotoAcceptsOnlyRealImages(t *testing.T) {
	s := NewStore()
	for name, data := range map[string][]byte{"png": png, "jpeg": jpeg, "webp": webp} {
		got, err := s.SetPhoto("acme", "ada", data)
		if err != nil || !got.HasPhoto {
			t.Fatalf("%s: %v", name, err)
		}
		back, mime, _ := s.Photo("acme", "ada")
		if !bytes.Equal(back, data) || mime != "image/"+name {
			t.Fatalf("%s came back as %s", name, mime)
		}
	}
	for name, data := range map[string][]byte{
		"svg":           []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html":          []byte(`<html><script>alert(1)</script></html>`),
		"gif":           []byte("GIF89a" + string(make([]byte, 32))),
		"text":          []byte("just text"),
		"empty":         {},
		"too big":       append(bytes.Clone(png), make([]byte, MaxPhoto)...),
		"png then html": append([]byte("<html>"), png...),
	} {
		if _, err := s.SetPhoto("acme", "ada", data); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}
}

func TestPhotoVersionChanges(t *testing.T) {
	s := NewStore()
	a, _ := s.SetPhoto("acme", "ada", png)
	b, _ := s.SetPhoto("acme", "ada", jpeg)
	c := s.ClearPhoto("acme", "ada")
	if a.PhotoVersion == b.PhotoVersion || b.PhotoVersion == c.PhotoVersion || c.HasPhoto {
		t.Fatalf("versions %d %d %d, has photo %v", a.PhotoVersion, b.PhotoVersion, c.PhotoVersion, c.HasPhoto)
	}
	if _, _, err := s.Photo("acme", "ada"); !errors.Is(err, ErrNoPhoto) {
		t.Fatalf("a cleared photo is still served: %v", err)
	}
}
