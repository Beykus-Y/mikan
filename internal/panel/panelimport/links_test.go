package panelimport

import (
	"strings"
	"testing"
)

// The bot reads the path of an old link as the router and the handler do.
func TestLinkToken(t *testing.T) {
	for path, want := range map[string]string{
		"/sub/abc":                         "abc",
		"/sub/abc/":                        "abc",
		"/sub/abc/clash":                   "abc",
		"/sub/abc/clash/more":              "abc",
		"/sub/":                            "",
		"/sub":                             "",
		"/subx/abc":                        "",
		"/other/abc":                       "",
		"/x/sub/abc":                       "",
		"//sub/abc":                        "",
		"/sub//abc":                        "",
		"/sub/./abc":                       "",
		"/sub/../sub/abc":                  "",
		"/sub/" + strings.Repeat("a", 513): "",
	} {
		got, _, ok := LinkToken(path, "sub")
		if got != want || ok != (want != "") {
			t.Errorf("LinkToken(%q) = %q %v, want %q", path, got, ok, want)
		}
	}
	if token, rest, ok := LinkToken("/api/sub/abc/mihomo", "api/sub"); !ok || token != "abc" || rest != "mihomo" {
		t.Errorf("a path of two segments: %q %q %v", token, rest, ok)
	}
	if _, _, ok := LinkToken("/sub/abc", ""); ok {
		t.Error("old links off, yet a token")
	}
}
