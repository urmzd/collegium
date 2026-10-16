package mockidp

import (
	"html/template"
	"net/http"
	"net/url"
)

type pageData struct {
	Error    string
	Issuer   string
	People   []Person
	Machines []Machine
	Hidden   map[string]string
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Development sign-in</title>
<style>
body { font: 15px/1.5 system-ui, sans-serif; max-width: 40rem; margin: 2rem auto; padding: 0 1rem; color: #1a1a1a; background: #fff; }
.warn { border: 2px solid #b42318; background: #fef3f2; color: #7a271a; padding: .75rem 1rem; border-radius: 6px; }
button { display: block; width: 100%; text-align: left; margin: .5rem 0; padding: .75rem 1rem; font: inherit; background: #f5f5f5; border: 1px solid #ccc; border-radius: 6px; cursor: pointer; }
button:hover { background: #ebebeb; }
small, code { color: #555; }
</style>
</head>
<body>
<p class="warn"><strong>Development identity provider.</strong> This is not a real sign-in. There are no passwords: anyone who can reach this page can sign in as anyone listed. It only runs on this machine's loopback address{{if .Issuer}} ({{.Issuer}}){{end}}.</p>
{{if .Error}}
<h1>Sign-in request refused</h1>
<p>{{.Error}}</p>
{{else}}
<h1>Sign in as</h1>
<form method="post" action="/authorize">
{{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">
{{end}}
{{range .People}}<button type="submit" name="persona" value="{{.Subject}}">
<strong>{{.Name}}</strong> <small>{{.Email}}</small><br>
<small>subject <code>{{.Subject}}</code>{{if .Groups}}, groups {{range $i, $g := .Groups}}{{if $i}}, {{end}}<code>{{$g}}</code>{{end}}{{else}}, no groups{{end}}{{if .Tenant}}, tenant <code>{{.Tenant}}</code>{{end}}</small>
</button>
{{end}}
</form>
{{if .Machines}}
<h2>Machines</h2>
<p>These get a token with client credentials at <code>{{.Issuer}}/token</code>, not here.</p>
<ul>{{range .Machines}}<li><code>{{.ClientID}}</code>: {{range $i, $s := .Scopes}}{{if $i}}, {{end}}<code>{{$s}}</code>{{end}}</li>{{end}}</ul>
{{end}}
{{end}}
</body>
</html>
`))

func (s *Server) page(w http.ResponseWriter, status int, d pageData) {
	if d.Issuer == "" {
		d.Issuer = s.issuer
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	// A browser applies form-action to the redirect that follows a submit,
	// so the registered redirect origins are listed too.
	actions := "'self'"
	for _, r := range s.cfg.RedirectURIs {
		if u, err := url.Parse(r); err == nil {
			actions += " " + u.Scheme + "://" + u.Host
		}
	}
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action "+actions+"; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, d)
}
