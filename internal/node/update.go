package node

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"mikan/internal/fsutil"
	"mikan/internal/nodeapi"
	"mikan/internal/release"
)

// The node meets the updater on its server through files in its data directory, which the
// server sees as /opt/mikan/data/node/update (a path unit there starts `mikan update
// --requested` when the request appears):
//
//	update/request      the node writes {"version": "X"} for the panel's Update button; the
//	                    updater removes it first and updates only to a signed release of
//	                    that version
//	update/status.json  the updater writes {"state": "running|ok|failed", "version",
//	                    "from", "error", "at"}; the node shows it in its health
//
// The node is not trusted by its server, and the server is trusted by the node.
const (
	updateDir  = "update"
	updateAsk  = "request"
	updateDone = "status.json"
	// maxUpdateStatus is far above what the updater writes (a short error and four fields).
	maxUpdateStatus = 16 << 10
)

// ErrBadVersion is a version that is no release version.
var ErrBadVersion = errors.New("not a release version")

// RequestUpdate asks the updater on the node's server for a release: the request file
// appears whole or not at all, and the status of an earlier update goes, so that the
// panel does not read its ending as this one's.
func (e *Engine) RequestUpdate(version string) error {
	if !release.Valid(version) {
		return ErrBadVersion
	}
	dir := filepath.Join(e.dataDir, updateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"version": version})
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, updateDone)); err != nil && !errors.Is(err, os.ErrNotExist) {
		e.log.Warn("update: cannot clear the old status", "err", err)
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, updateAsk), body, 0o644)
}

// UpdateStatus is what the updater wrote about its last update, nil when there is none or
// the file is not one: a missing, oversized, damaged or unreadable file is no news.
func (e *Engine) UpdateStatus() *nodeapi.UpdateStatus {
	return readUpdateStatus(filepath.Join(e.dataDir, updateDir, updateDone))
}

func readUpdateStatus(path string) *nodeapi.UpdateStatus {
	// Not through os.ReadFile: a link or a FIFO planted at the path must not be followed or waited for.
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxUpdateStatus {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxUpdateStatus+1))
	if err != nil || len(raw) > maxUpdateStatus {
		return nil
	}
	var s nodeapi.UpdateStatus
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	clean, ok := s.Clean()
	if !ok {
		return nil
	}
	return &clean
}
