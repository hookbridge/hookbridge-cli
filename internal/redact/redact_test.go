package redact

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestURL(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		secret string
	}{
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "no userinfo unchanged",
			in:   "http://localhost:3000",
			want: "http://localhost:3000",
		},
		{
			// The username is deliberately not preserved. Keeping it would mean
			// trusting net/url to tell us where the userinfo ends, and a credential
			// containing "/" stops being userinfo per RFC 3986 while still being a
			// secret the user typed. A username is often a token anyway, so it goes
			// too. Do not "restore" it — that reintroduces the leak.
			name:   "username and password",
			in:     "https://user:pass@example.com/hook",
			want:   "https://REDACTED@example.com/hook",
			secret: "pass",
		},
		{
			name:   "username only",
			in:     "https://token@example.com/hook",
			want:   "https://REDACTED@example.com/hook",
			secret: "token",
		},
		{
			name:   "port path and query preserved",
			in:     "https://user:pass@example.com:8443/hook/path?foo=bar&baz=qux",
			want:   "https://REDACTED@example.com:8443/hook/path?foo=bar&baz=qux",
			secret: "pass",
		},
		{
			name:   "unparseable string containing user:pass@ fails closed",
			in:     "http://user:pass@exa mple.com/hook",
			want:   "http://REDACTED@exa mple.com/hook",
			secret: "pass",
		},
		{
			name:   "password with percent-escaped special characters",
			in:     "https://user:p%40%24%24%3Aword%21@example.com/hook",
			want:   "https://REDACTED@example.com/hook",
			secret: "word",
		},
		{
			name:   "schemeless opaque credential-like string",
			in:     "user:pass@example.com/hook",
			want:   "REDACTED@example.com/hook",
			secret: "pass",
		},
		{
			name:   "scheme with colon but no slashes leaks via opaque",
			in:     "http:user:pass@example.com/hook",
			want:   "REDACTED@example.com/hook",
			secret: "pass",
		},
		{
			name:   "mailto opaque URL with credential-like userinfo",
			in:     "mailto:user:pass@example.com",
			want:   "REDACTED@example.com",
			secret: "pass",
		},
		{
			name:   "fail-closed fallback when password itself contains ://",
			in:     "http://us:pa://ss@host.com/hook",
			want:   "http://REDACTED@host.com/hook",
			secret: "pa://ss",
		},
		{
			name:   "decoy :// inside password on opaque mailto scheme",
			in:     "mailto:us:pa://ss@host.com",
			want:   "REDACTED@host.com",
			secret: "pa://ss",
		},
		{
			name:   "decoy :// inside password on opaque smtp scheme",
			in:     "smtp:admin:s3cr3t://token@internal-host/hook",
			want:   "REDACTED@internal-host/hook",
			secret: "s3cr3t://token",
		},
		{
			name:   "protocol-relative authority with credentials",
			in:     "//user:pass@host/hook",
			want:   "REDACTED@host/hook",
			secret: "pass",
		},
		{
			name:   "slash inside userinfo before colon defeats authority boundary",
			in:     "http://us/er:pass@host/hook",
			want:   "http://REDACTED@host/hook",
			secret: "pass",
		},
		{
			name:   "digits-then-slash inside userinfo mimics a port",
			in:     "http://us:443/token@internal.example.com/webhook",
			want:   "http://REDACTED@internal.example.com/webhook",
			secret: "token",
		},
		{
			name:   "protocol-relative authority with slash inside userinfo",
			in:     "//foo/bar:baz@qux/hook",
			want:   "REDACTED@qux/hook",
			secret: "baz",
		},
		{
			// Intentional regression: a legitimate "@" in a query string (not a
			// credential) still gets swallowed by redaction because we no longer
			// trust URL structure to tell secrets from data. Do not "fix" this by
			// reintroducing structural parsing — that is exactly what kept leaking
			// real credentials across five rounds of review. A mangled display
			// string beats a leaked password.
			name: "legitimate @ in query string is intentionally mangled",
			in:   "http://host/p?email=a@b.com",
			want: "http://REDACTED@b.com",
		},
		{
			name:   "protocol-relative authority with decoy :// inside password",
			in:     "//user:pa://ss@example.com/hook",
			want:   "REDACTED@example.com/hook",
			secret: "pa://ss",
		},
		{
			name: "username with empty password and trailing colon",
			in:   "token:@host",
			want: "REDACTED@host",
		},
		{
			name: "schemeless string with no @ at all stays unchanged",
			in:   "localhost:9000/hook",
			want: "localhost:9000/hook",
		},
		{
			name: "bare host and path with no scheme or userinfo unchanged",
			in:   "example.com/hook",
			want: "example.com/hook",
		},
		{
			name: "opaque URL with no credentials unchanged",
			in:   "urn:isbn:0451450523",
			want: "urn:isbn:0451450523",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := URL(tt.in)
			assert.Equal(t, tt.want, got)
			if tt.secret != "" {
				assert.NotContains(t, got, tt.secret, "redacted output must never contain the credential")
			}
		})
	}
}

func TestURL_RedactedPlaceholderIsLiteralNotPercentEscaped(t *testing.T) {
	got := URL("https://user:pass@example.com/hook")
	assert.Contains(t, got, "REDACTED")
	assert.NotContains(t, got, "%")
}

func TestURL_NeverLeaksCredential(t *testing.T) {
	got := URL("https://user:hunter2@example.com/hook")
	assert.NotContains(t, got, "hunter2")
}

func TestURL_UnparseableNeverLeaksRawUserinfo(t *testing.T) {
	got := URL("http://user:pass@exa mple.com/hook")
	assert.NotContains(t, got, "user:pass@")
	assert.False(t, strings.Contains(got, "pass@"))
}

func TestURL_NoCredentialEverSurvives(t *testing.T) {
	schemes := []string{"http://", "https://", "http:", "mailto:", "smtp:", "//", ""}
	usernames := []string{"user", "admin", "token", "", "us/er", "svc:8080/deploytoken"}
	passwords := []string{"pass", "hunter2", "pa://ss", "p@ss", "p:ss", "p://q", "s3cr3t://token", "p%40ss", "pa/ss", "443/token", "tok/en:9000", ""}
	hostpaths := []string{"example.com/hook", "localhost:9000/hook", "[::1]:8080/p", "host/p?email=a@b.com"}

	checked := 0
	leaked := 0
	for _, scheme := range schemes {
		for _, user := range usernames {
			for _, hostpath := range hostpaths {
				userOnly := scheme + user + "@" + hostpath
				URL(userOnly)

				for _, pass := range passwords {
					in := scheme + user + ":" + pass + "@" + hostpath
					got := URL(in)
					if pass == "" {
						continue
					}
					checked++
					if strings.Contains(got, pass) {
						leaked++
						t.Errorf("input %q leaked password %q in output %q", in, pass, got)
					}
				}
			}
		}
	}
	t.Logf("checked %d password-bearing generated inputs, %d leaked", checked, leaked)
}
