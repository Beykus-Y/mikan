package panelimport

import "testing"

// The tokens below were made by Marzban's and PasarGuard's own code (app/utils/jwt.py,
// create_subscription_token) with this secret.
const testSecret = "s3cr3t-key-from-jwt-table"

func TestVerifierMarzban(t *testing.T) {
	v := Verifier{Kind: Marzban, Secret: testSecret}
	for token, want := range map[string]string{
		"aXZhbl9wZXRyb3YsMTc1OTQwMDAwMAUGNuNGBJks": "name:ivan_petrov",
		"YUBiLmMsMTcwMDAwMDAwMQbWYcPerydd":         "name:a@b.c",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJpdmFuX3BldHJvdiIsImFjY2VzcyI6InN1YnNjcmlwdGlvbiIsImlhdCI6MTcwMDAwMDAwMH0.TZ3xx4BM_XaIEGDNJ__mG9s9DMuE29ZON8OwYqzyiqk": "name:ivan_petrov",
	} {
		if got, issued, ok := v.Who(token); !ok || got != want || issued < 1_600_000_000 {
			t.Errorf("Who(%q) = %q %d %v, want %q", token, got, issued, ok, want)
		}
	}
	// PasarGuard's own formats are not Marzban's.
	for _, token := range []string{"djMsNDIsMTc1OTQwMDAwMA.fa-02Knno9F0qsz39WtypLtudEubWHfKFrYoyF8skR4", "b2xnYSwxNzU5NDAwMDAw5ea3b3f821"} {
		if _, _, ok := v.Who(token); ok {
			t.Errorf("Marzban took %q", token)
		}
	}
}

func TestVerifierPasarGuard(t *testing.T) {
	v := Verifier{Kind: PasarGuard, Secret: testSecret}
	for token, want := range map[string]string{
		"djMsNDIsMTc1OTQwMDAwMA.fa-02Knno9F0qsz39WtypLtudEubWHfKFrYoyF8skR4": "id:42",
		"b2xnYSwxNzU5NDAwMDAw5ea3b3f821":                                     "name:olga", // hex signature
		"aXZhbl9wZXRyb3YsMTc1OTQwMDAwMAUGNuNGBJks":                           "name:ivan_petrov",
	} {
		if got, issued, ok := v.Who(token); !ok || got != want || issued != 1759400000 {
			t.Errorf("Who(%q) = %q %d %v, want %q", token, got, issued, ok, want)
		}
	}
}

// A token made up from a username, or signed with another secret, is nobody's.
func TestVerifierRefusesForgeries(t *testing.T) {
	good := "aXZhbl9wZXRyb3YsMTc1OTQwMDAwMAUGNuNGBJks"
	for _, v := range []Verifier{{Kind: Marzban, Secret: "another secret"}, {Kind: Marzban}} {
		if _, _, ok := v.Who(good); ok {
			t.Errorf("%+v took a token signed with another secret", v)
		}
	}
	v := Verifier{Kind: PasarGuard, Secret: testSecret}
	for _, token := range []string{
		"aXZhbl9wZXRyb3YsMTc1OTQwMDAwMAAAAAAAAAAA",                           // the payload with a guessed signature
		"djMsNDIsMTc1OTQwMDAwMA.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // id 42, no signature
		"djMsNDMsMTc1OTQwMDAwMA.fa-02Knno9F0qsz39WtypLtudEubWHfKFrYoyF8skR4", // id 43 with 42's signature
		"short",
		"",
	} {
		if got, _, ok := v.Who(token); ok {
			t.Errorf("Who(%q) = %q, a forgery passed", token, got)
		}
	}
}
