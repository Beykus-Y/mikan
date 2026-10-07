package subs

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// TitleVars are the variables the profile title and the announcement may hold, the same
// way the bot's texts do: {brand} · {name} · until {date}. Any other {word} stays as text.
var TitleVars = []string{"brand", "name", "date", "days", "used", "left", "total"}

// TitleMax bounds a filled title or announcement: apps show one line.
const TitleMax = 200

// msk is the zone dates are written in, as the bot writes them: most users live there.
var msk = time.FixedZone("MSK", 3*60*60)

// titleValues are the variables for one user. A term or limit that is not set reads "∞".
func titleValues(u db.User, grants int64, cfg Config, now time.Time) map[string]string {
	en := cfg.Lang == "en"
	v := map[string]string{"brand": cfg.Brand, "name": u.Name, "date": "∞", "days": "∞",
		"used": sizeText(u.UsedUp+u.UsedDown, en), "left": "∞", "total": "∞"}
	if u.ExpiresAt.Valid {
		exp := time.Unix(u.ExpiresAt.Int64, 0)
		v["date"] = exp.In(msk).Format("02.01.2006")
		days := int64(0)
		if left := exp.Sub(now); left > 0 {
			days = int64((left + 24*time.Hour - 1) / (24 * time.Hour))
		}
		v["days"] = strconv.FormatInt(days, 10)
	}
	if u.TrafficLimit.Valid && u.TrafficLimit.Int64 > 0 {
		v["left"] = sizeText(domain.TrafficLeft(u.TrafficLimit, u.UsedUp+u.UsedDown, grants), en)
		total := u.TrafficLimit.Int64
		if grants > 0 {
			total = max(total, u.UsedUp+u.UsedDown) + grants
		}
		v["total"] = sizeText(total, en)
	}
	return v
}

// fillTitle puts the user's values in place of the variables, in one pass: a value that
// holds {something} itself is not filled again. The result is one line of at most
// TitleMax letters.
func fillTitle(s string, vars map[string]string) string {
	var out strings.Builder
	rest := s
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			break
		}
		if v, ok := vars[rest[i+1:i+j]]; ok {
			out.WriteString(rest[:i])
			out.WriteString(v)
			rest = rest[i+j+1:]
			continue
		}
		out.WriteString(rest[:i+1])
		rest = rest[i+1:]
	}
	out.WriteString(rest)
	line := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, out.String())
	return cut(line, TitleMax)
}

// profileTitle is the name the apps show for the profile: the title filled for the user,
// the brand when there is none or it fills to nothing (just {name} of a user without one).
func profileTitle(cfg Config, vars map[string]string) string {
	if cfg.Title != "" {
		if filled := fillTitle(cfg.Title, vars); strings.TrimSpace(filled) != "" {
			return filled
		}
	}
	return cfg.Brand
}

// fileName makes a profile title a file name: no path separators or characters Windows
// refuses, and never empty.
func fileName(title string) string {
	name := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return ' '
		}
		return r
	}, title))
	if name == "" {
		return "mikan"
	}
	return name
}

// sizeText writes bytes the way the bot does: 12,5 ГБ or 12.5 GB.
func sizeText(n int64, en bool) string {
	units, sep := []string{"Б", "КБ", "МБ", "ГБ", "ТБ"}, ","
	if en {
		units, sep = []string{"B", "KB", "MB", "GB", "TB"}, "."
	}
	v, i := float64(max(n, 0)), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	s := fmt.Sprintf("%.0f", v)
	if i > 0 && v < 100 {
		s = strings.Replace(strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0"), ".", sep, 1)
	}
	return s + " " + units[i]
}
