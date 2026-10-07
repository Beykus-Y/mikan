// Package torrent holds the torrent blocker's settings. The nodes do the catching (see
// internal/node/bittorrent.go); the panel stores what they report, keeps a caught user
// out of every node for the ban, and tells the admin.
package torrent

import (
	"context"
	"errors"
	"slices"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/settings"
)

const KeyConfig = "torrent_block"

// The ban's bounds, in minutes: up to 30 days.
const MaxBanMinutes = 30 * 24 * 60

// MaxExempt bounds the list of users the blocker leaves alone.
const MaxExempt = 1000

var ErrConfig = errors.New("torrent_config")

type Config struct {
	Enabled bool `json:"enabled"`
	// BanMinutes keeps a caught user off every node for so long; 0: only what was caught
	// is dropped.
	BanMinutes int64 `json:"ban_minutes"`
	// Exempt are the users left alone, by id.
	Exempt []int64 `json:"exempt"`
}

func Default() Config { return Config{BanMinutes: 60, Exempt: []int64{}} }

// Validate keeps the ban within bounds and the exempt list sorted, without repeats.
func (c *Config) Validate() error {
	if c.BanMinutes < 0 || c.BanMinutes > MaxBanMinutes || len(c.Exempt) > MaxExempt {
		return ErrConfig
	}
	if c.Exempt == nil {
		c.Exempt = []int64{}
	}
	slices.Sort(c.Exempt)
	c.Exempt = slices.Compact(c.Exempt)
	return nil
}

func Load(ctx context.Context, s *settings.Settings) (Config, error) {
	c, _, err := settings.GetOver(ctx, s, KeyConfig, Default())
	if c.Exempt == nil {
		c.Exempt = []int64{}
	}
	return c, err
}

// Block is what the nodes get: nil when the blocker is off.
func (c Config) Block() *nodeapi.TorrentBlock {
	if !c.Enabled {
		return nil
	}
	return &nodeapi.TorrentBlock{BanSeconds: c.BanMinutes * 60}
}

// IsExempt says whether the blocker leaves the user alone.
func (c Config) IsExempt(userID int64) bool {
	_, ok := slices.BinarySearch(c.Exempt, userID)
	return ok
}
