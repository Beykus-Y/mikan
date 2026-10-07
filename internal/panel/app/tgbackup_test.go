package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Telegram backups over the API: the session's only, a password before switching on,
// and the password never comes back.
func TestTelegramBackupOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	url := k.api + "/telegram/backup"

	if resp, body := k.asKey(k.full, http.MethodGet, "/telegram/backup", nil); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "session_only") {
		t.Fatalf("a full key reads the backup settings: %d %s", resp.StatusCode, body)
	}

	var v struct {
		Enabled      bool   `json:"enabled"`
		Hour         int    `json:"hour"`
		PasswordSet  bool   `json:"password_set"`
		AdminChatSet bool   `json:"admin_chat_set"`
		LastError    string `json:"last_error"`
	}
	resp, body := k.do(http.MethodGet, url, nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.Enabled || v.Hour != 3 || v.PasswordSet || v.AdminChatSet {
		t.Fatalf("defaults: %d %s", resp.StatusCode, body)
	}

	resp, body = k.do(http.MethodPatch, url, map[string]any{"enabled": true}, k.csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "no_backup_password") {
		t.Fatalf("on without a password: %d %s", resp.StatusCode, body)
	}
	resp, body = k.do(http.MethodPatch, url, map[string]any{"password": "short"}, k.csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "backup_password_short") {
		t.Fatalf("a short password: %d %s", resp.StatusCode, body)
	}

	const pw = "a long enough password"
	resp, body = k.do(http.MethodPatch, url, map[string]any{"enabled": true, "password": pw, "hour": 5}, k.csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || !v.Enabled || v.Hour != 5 || !v.PasswordSet {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), pw) {
		t.Fatal("the password came back")
	}

	// No admin chat is connected: there is nowhere to send to.
	resp, body = k.do(http.MethodPost, url+"/send", nil, k.csrf)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "no_admin_chat") {
		t.Fatalf("send without a chat: %d %s", resp.StatusCode, body)
	}
	// That is said at once, before anything runs in the background.
	resp, body = k.do(http.MethodGet, url, nil, nil)
	if json.Unmarshal(body, &v) != nil || v.LastError != "" || strings.Contains(string(body), `"sending":true`) {
		t.Fatalf("a refused send started a backup: %s", body)
	}
	// 19 characters are not enough: the file stays in the chat's history for good.
	resp, body = k.do(http.MethodPatch, url, map[string]any{"password": strings.Repeat("x", 19)}, k.csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "backup_password_short") {
		t.Fatalf("a 19-character password: %d %s", resp.StatusCode, body)
	}
}
