package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
)

// indexTable is testdata/index.json, which the installer's tests read as well
// (installer/src/release.rs): both sides keep, skip and choose the same entries.
type indexTable struct {
	Decode []struct {
		Name     string
		Doc      string
		Versions []string
		Error    bool
	}
	Choose []struct {
		Releases []Entry
		Cases    []struct {
			Current  string
			Beta     bool
			Target   string
			Newest   string
			Manifest string
		}
	}
}

func readIndexTable(t *testing.T) indexTable {
	t.Helper()
	data, err := os.ReadFile("../../testdata/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var table indexTable
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Decode) == 0 || len(table.Choose) == 0 {
		t.Fatal("empty index table")
	}
	return table
}

func TestIndexDecodeTable(t *testing.T) {
	for _, c := range readIndexTable(t).Decode {
		ix, err := DecodeIndex([]byte(c.Doc))
		if c.Error {
			if err == nil {
				t.Errorf("%s: decoded %+v", c.Name, ix)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		got := []string{}
		for _, e := range ix.Releases {
			got = append(got, e.Version)
		}
		if !slices.Equal(got, c.Versions) {
			t.Errorf("%s: kept %q, want %q", c.Name, got, c.Versions)
		}
	}
}

func TestChooseTable(t *testing.T) {
	for i, table := range readIndexTable(t).Choose {
		for _, c := range table.Cases {
			got := Choose(table.Releases, c.Current, c.Beta)
			if got.Target.Version != c.Target || got.Newest.Version != c.Newest {
				t.Errorf("table %d, %q beta=%v: target %q newest %q, want %q and %q", i, c.Current, c.Beta, got.Target.Version, got.Newest.Version, c.Target, c.Newest)
			}
			if c.Manifest != "" && got.Target.Manifest != c.Manifest {
				t.Errorf("table %d, %q beta=%v: manifest %q, want %q", i, c.Current, c.Beta, got.Target.Manifest, c.Manifest)
			}
			if hop := c.Target != "" && c.Target != c.Newest; got.Hop() != hop {
				t.Errorf("table %d, %q beta=%v: hop %v", i, c.Current, c.Beta, got.Hop())
			}
		}
	}
}

// The index is believed only with the release key's signature over its exact bytes.
func TestParseIndexChecksTheSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data := []byte(`{"schema":1,"published":"2026-10-04T10:00:00Z","releases":[{"version":"0.5.0.1","channel":"stable","manifest":"https://x/m.json","from":"0.4.5"}]}`)
	ix, err := ParseIndex(data, Sign(data, priv)+"\n", pub)
	if err != nil || len(ix.Releases) != 1 || ix.Schema != IndexSchema || ix.Published.IsZero() {
		t.Fatalf("signed: %+v %v", ix, err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := ParseIndex(data, Sign(data, priv), other); !errors.Is(err, ErrSignature) {
		t.Fatalf("another key: %v", err)
	}
	tampered := []byte(string(data[:len(data)-3]) + `"}]}`)
	if _, err := ParseIndex(tampered, Sign(data, priv), pub); !errors.Is(err, ErrSignature) {
		t.Fatalf("a changed index: %v", err)
	}
	if _, err := ParseIndex(data, "not base64!", pub); !errors.Is(err, ErrSignature) {
		t.Fatalf("garbage signature: %v", err)
	}
	if _, err := DecodeIndex([]byte("{\"releases\":[],\"x\":\"\xff\"}")); err == nil {
		t.Fatal("an index that is not UTF-8 is read")
	}
}

func TestChannels(t *testing.T) {
	for v, want := range map[string]string{"0.5.0.1": Stable, "0.4.5": Stable, "0.5.0.1-rc.1": Beta, "0.5.0.1-beta": Beta, "dev": Stable} {
		if got := ChannelOf(v); got != want {
			t.Errorf("ChannelOf(%q) = %q", v, got)
		}
	}
	for _, s := range []string{"stable", "beta"} {
		if !ValidChannel(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range []string{"", "Stable", "BETA", " beta", "beta\n", "nightly", "stable,beta"} {
		if ValidChannel(s) {
			t.Errorf("%q accepted", s)
		}
	}
}
