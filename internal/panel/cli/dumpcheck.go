package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// dumpEntryTypes are the kinds of object a dump of the panel's schema holds. pg_restore
// runs what an archive's entries say as the database role it connects as, so an archive
// with anything else (a function, a trigger, an extension, a rule) is refused before it
// runs: a backup that came from a chat or a download must not bring code along.
var dumpEntryTypes = []string{
	// Longest first: "TABLE DATA" before "TABLE".
	"SEQUENCE OWNED BY", "FK CONSTRAINT", "SEQUENCE SET", "TABLE DATA",
	"CONSTRAINT", "SEQUENCE", "DEFAULT", "COMMENT", "SCHEMA", "TABLE", "INDEX",
}

// checkDump lists an archive's entries (pg_restore -l reads the file alone) and refuses
// one with an entry the panel's own dumps never have.
func checkDump(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "pg_restore", "--list", path)
	cmd.Env = []string{} // reads a file, needs nothing from the panel
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("pg_restore --list failed: %w%s", err, toolOutput("", stderr.String()))
	}
	return checkDumpList(out)
}

func checkDumpList(list []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(list))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		// "<id>; <catalog oid> <oid> <TYPE> <schema> <name> <owner>"
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ";") {
			return fmt.Errorf("the archive has an entry this panel cannot read: %q", line)
		}
		rest := strings.Join(fields[3:], " ")
		known := false
		for _, typ := range dumpEntryTypes {
			if rest == typ || strings.HasPrefix(rest, typ+" ") {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("the archive is not a dump of this panel's tables: it has %q", rest)
		}
	}
	return sc.Err()
}
