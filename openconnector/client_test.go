package openconnector

import "testing"

// The Authorization header format depends on the credential type — an API key
// ("api-…") must be sent RAW, a runtime token ("oct_…") as Bearer, and an
// already-schemed value untouched. Prepending Bearer to an API key breaks auth.
func TestAuthHeader(t *testing.T) {
	cases := map[string]string{
		"api-c09b60a4ed":  "api-c09b60a4ed",       // API key: raw, no Bearer
		"oct_abc123":      "Bearer oct_abc123",    // runtime token: Bearer
		"Bearer already":  "Bearer already",       // already schemed: verbatim
		" oct_trim ":      "Bearer oct_trim",      // trimmed
		"":                "",                     // empty: no header
		"opaque-token-xy": "opaque-token-xy",      // unknown format: raw
	}
	for in, want := range cases {
		if got := authHeader(in); got != want {
			t.Errorf("authHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

// A base URL pointing at the web console is corrected to the API host.
func TestConsoleHostRewrite(t *testing.T) {
	c := New("https://console.oomol.com", "api-x")
	if c.BaseURL != "https://connector.oomol.com" {
		t.Errorf("BaseURL = %q, want https://connector.oomol.com", c.BaseURL)
	}
}
