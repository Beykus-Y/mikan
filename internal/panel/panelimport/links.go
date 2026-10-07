package panelimport

import (
	"context"
	"path"
	"strings"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// MaxToken is the longest old token taken: the HTTP route and the bot refuse longer ones.
const MaxToken = 512

// User finds whose an old link is. A token the old panel gave out unchanged (Remnawave's
// short UUID) is looked up as it is; a token Marzban or PasarGuard signed is checked with
// the old panel's secret, and the name or id inside it is looked up. The keys those are
// filed under ("name:…", "id:…") are never taken from the link itself.
func (v Verifier) User(ctx context.Context, q *db.Queries, token string) (db.User, bool) {
	if !strings.Contains(token, ":") {
		if row, err := q.LegacySubTokenUser(ctx, token); err == nil {
			return row.User, true
		}
	}
	if v.Secret == "" {
		return db.User{}, false
	}
	key, issued, ok := v.Who(token)
	if !ok {
		return db.User{}, false
	}
	row, err := q.LegacySubTokenUser(ctx, key)
	// A token made before the user was (made again under the same name) is refused, as
	// the old panel refuses it.
	if err != nil || issued < row.NotBefore {
		return db.User{}, false
	}
	return row.User, true
}

// LoadLegacy reads how the old links are set up: the path they lie under ("" when they
// are off) and what checks the signed ones.
func LoadLegacy(ctx context.Context, s *settings.Settings) (legacyPath string, v Verifier, err error) {
	if legacyPath, err = s.String(ctx, settings.KeyLegacySubPath); err != nil {
		return "", Verifier{}, err
	}
	kind, err := s.String(ctx, settings.KeyLegacySubKind)
	if err != nil {
		return "", Verifier{}, err
	}
	v.Kind = Kind(kind)
	if v.Secret, err = s.String(ctx, settings.KeyLegacySubSecret); err != nil {
		return "", Verifier{}, err
	}
	return legacyPath, v, nil
}

// LinkToken is the token in the path of an old link, and what follows it ("clash" in
// /sub/<token>/clash). The path must lie under the old panel's path (settings.Paths.Legacy)
// the way the router takes it, and the token is what the handler takes after that.
func LinkToken(urlPath, legacyPath string) (token, rest string, ok bool) {
	// The router turns away non-canonical paths ("//", "/./", "/../") and takes the old
	// panel's path as whole segments only.
	if legacyPath == "" || urlPath == "" || urlPath[0] != '/' || (path.Clean(urlPath) != urlPath && path.Clean(urlPath)+"/" != urlPath) {
		return "", "", false
	}
	if !strings.HasPrefix(urlPath, "/"+legacyPath+"/") {
		return "", "", false
	}
	token, rest, _ = strings.Cut(strings.TrimPrefix(path.Clean(urlPath[len(legacyPath)+1:]), "/"), "/")
	if token == "" || len(token) > MaxToken {
		return "", "", false
	}
	return token, rest, true
}
