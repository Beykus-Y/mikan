package tgbot

import (
	"strings"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// Tokens made by Marzban's and PasarGuard's own code with this secret (as in panelimport).
const (
	legacySecret = "s3cr3t-key-from-jwt-table"
	marzbanToken = "aXZhbl9wZXRyb3YsMTc1OTQwMDAwMAUGNuNGBJks"                           // name:ivan_petrov, made at 1759400000
	pasarToken   = "djMsNDIsMTc1OTQwMDAwMA.fa-02Knno9F0qsz39WtypLtudEubWHfKFrYoyF8skR4" // id:42
	remnaToken   = "k3Lm9PqRs2TuVw4x"
)

// The addresses of the panel the users came from lead to their subscriptions in the bot
// as they do over HTTP: the same tokens, the same suffixes, nothing else.
func TestBotOldPanelLinks(t *testing.T) {
	e := setup(t)
	ctx := e.ctx
	// A chat of its own for every message: a chat that says 20 in ten seconds is a flood.
	chat := int64(600)
	pool := domain.NewPool(e.st, e.clock)
	tariffs, _ := e.st.Q.ListTariffs(ctx)
	users := domain.NewUsers(e.st, pool, noChanges{}, e.clock)
	mk := func(name string, tokens map[string]string) db.User {
		t.Helper()
		u, err := users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		for tok, source := range tokens {
			if _, err := e.st.Q.AddLegacySubToken(ctx, db.AddLegacySubTokenParams{Token: tok, UserID: u.ID, Source: source}); err != nil {
				t.Fatal(err)
			}
		}
		return u
	}
	ivan := mk("ivan_petrov", map[string]string{"name:ivan_petrov": "marzban"})
	pg := mk("pg_user", map[string]string{"name:pg_user": "pasarguard", "id:42": "pasarguard"})
	rema := mk("rema", map[string]string{remnaToken: "remnawave"})
	configure := func(path, kind, secret string) {
		t.Helper()
		for k, v := range map[string]string{settings.KeyLegacySubPath: path, settings.KeyLegacySubKind: kind, settings.KeyLegacySubSecret: secret} {
			if err := settings.Set(ctx, e.set, k, v); err != nil {
				t.Fatal(err)
			}
		}
	}

	const (
		linked  = "подключена"
		invalid = "Ссылка не подошла"
	)
	// send posts a message and returns the bot's answer; the user is unlinked first, so
	// every try starts the same.
	send := func(u db.User, msg string) string {
		t.Helper()
		_ = e.st.Q.UnlinkTg(ctx, u.ID)
		chat++
		e.later()
		n := e.tg.count()
		e.say(chat, msg)
		c, _ := find(e.tg.wait(t, n, "sendMessage"), "sendMessage")
		return text(c)
	}
	ownedBy := func(u db.User) bool {
		l, err := e.st.Q.GetTgLink(ctx, u.ID)
		return err == nil && l.TgID == chat
	}
	accepted := func(u db.User, msg string) {
		t.Helper()
		if got := send(u, msg); !strings.Contains(got, linked) || !ownedBy(u) {
			t.Errorf("%q: not linked: %q", msg, got)
		}
	}
	refused := func(u db.User, msg string) {
		t.Helper()
		if got := send(u, msg); !strings.Contains(got, invalid) || ownedBy(u) {
			t.Errorf("%q: should be refused with the invalid-link text: %q", msg, got)
		}
	}

	// Remnawave: the short UUID as it is, with the client types it puts after it.
	configure("api/sub", "", "")
	accepted(rema, "https://old.example.com/api/sub/"+remnaToken)
	accepted(rema, "вот: https://old.example.com/api/sub/"+remnaToken+"/mihomo?x=y#frag, спасибо")
	accepted(rema, "https://old.example.com:8443/api/sub/"+remnaToken+"/info")
	refused(rema, "https://old.example.com/api/sub/"+remnaToken+"x") // not the token
	refused(rema, "https://old.example.com/sub/"+remnaToken)         // not the old panel's path
	refused(rema, "https://old.example.com/api/sub//"+remnaToken)    // the router turns it away too
	// Remnawave's tokens need no secret, but nothing signed passes without one.
	refused(ivan, "https://old.example.com/api/sub/"+marzbanToken)

	// Marzban: signed, checked with the old panel's secret.
	configure("sub", "marzban", legacySecret)
	accepted(ivan, "https://old.example.com/sub/"+marzbanToken)
	accepted(ivan, "https://old.example.com/sub/"+marzbanToken+"/clash")
	accepted(ivan, "https://old.example.com/sub/"+marzbanToken+"/?x=y")
	accepted(ivan, "https://old.example.com/sub/"+marzbanToken+"?x=y")
	refused(ivan, "https://old.example.com/sub/"+marzbanToken[:len(marzbanToken)-1]+"A") // a forged signature
	configure("sub", "marzban", "another secret")
	refused(ivan, "https://old.example.com/sub/"+marzbanToken)
	configure("sub", "marzban", "")
	refused(ivan, "https://old.example.com/sub/"+marzbanToken)

	// PasarGuard: the id inside the token.
	configure("sub", "pasarguard", legacySecret)
	accepted(pg, "https://old.example.com/sub/"+pasarToken)
	accepted(pg, "https://old.example.com/sub/"+pasarToken+"/v2ray?x=y")
	configure("sub", "marzban", legacySecret)
	refused(pg, "https://old.example.com/sub/"+pasarToken) // PasarGuard's format is not Marzban's

	// A token made before the user was is refused as the old panel refuses it.
	configure("sub", "marzban", legacySecret)
	if err := e.st.Q.DeleteLegacySubTokensOf(ctx, ivan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Q.AddLegacySubToken(ctx, db.AddLegacySubTokenParams{Token: "name:ivan_petrov", UserID: ivan.ID, Source: "marzban", NotBefore: 1759400001}); err != nil {
		t.Fatal(err)
	}
	refused(ivan, "https://old.example.com/sub/"+marzbanToken)
	if err := e.st.Q.DeleteLegacySubTokensOf(ctx, ivan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Q.AddLegacySubToken(ctx, db.AddLegacySubTokenParams{Token: "name:ivan_petrov", UserID: ivan.ID, Source: "marzban", NotBefore: 1759400000}); err != nil {
		t.Fatal(err)
	}
	accepted(ivan, "https://old.example.com/sub/"+marzbanToken)

	// A reissue stops the old links.
	if _, err := users.Reissue(ctx, ivan.ID); err != nil {
		t.Fatal(err)
	}
	refused(ivan, "https://old.example.com/sub/"+marzbanToken)

	// Old links off: nothing of theirs is taken, not even a token that is on file.
	configure("", "", "")
	refused(rema, "https://old.example.com/api/sub/"+remnaToken)
	refused(rema, "https://old.example.com/sub/"+remnaToken)

	// The panel's own links go on working, with or without old ones set up.
	own, err := e.st.Q.GetUser(ctx, e.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	accepted(own, "https://vpn.example.com:21355/sub/"+own.SubToken)
	accepted(own, "https://vpn.example.com:21355/sub/"+own.SubToken+"/info")
	configure("sub", "marzban", legacySecret)
	accepted(own, "https://vpn.example.com:21355/sub/"+own.SubToken)

	// An address that leads nowhere is told so; a message with no address with a path,
	// or none, only brings the menu back.
	refused(own, "https://example.com/some/page")
	refused(own, "https://vpn.example.com:21355/sub/"+own.SubToken[:20])
	if got := send(own, "https://example.com"); strings.Contains(got, invalid) || strings.Contains(got, linked) {
		t.Errorf("an address without a path: %q", got)
	}
	if got := send(own, "где моя подписка?"); strings.Contains(got, invalid) || strings.Contains(got, linked) {
		t.Errorf("a message with no address: %q", got)
	}

	// An old link is no more proof of owning the subscription than the panel's own: one
	// held by another account asks its owner first.
	configure("api/sub", "", "")
	// The owner has a chat with the bot, so it can ask them.
	if err := e.st.Q.UpsertTgChat(ctx, db.UpsertTgChatParams{TgID: 777, FirstName: "Owner", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: rema.ID, TgID: 777, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	e.later()
	n := e.tg.count()
	chat++
	e.say(chat, "https://old.example.com/api/sub/"+remnaToken)
	c, _ := find(e.tg.wait(t, n, "sendMessage"), "sendMessage")
	if !strings.Contains(text(c), "Эта подписка уже подключена") || ownedBy(rema) {
		t.Errorf("a held subscription is taken without its owner: %q", text(c))
	}
}
