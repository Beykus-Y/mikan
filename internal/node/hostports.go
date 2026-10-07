package node

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/nodeapi"
)

// What listens on the node's server, read from the kernel's socket tables. With
// network_mode: host they are the server's, not the container's, so a program outside
// mikan (an Xray of another panel, nginx) shows in them as well.

// firstEphemeral is where the kernel hands out ports to outgoing connections when
// /proc/sys/net/ipv4/ip_local_port_range cannot be read.
const firstEphemeral = 32768

// hostPortsTTL: the panel asks every few seconds, and a table of a busy server is long.
const hostPortsTTL = 10 * time.Second

type hostPortsCache struct {
	mu   sync.Mutex
	at   time.Time
	last *nodeapi.HostPorts
}

// get returns the ports listening now, read at most every hostPortsTTL; nil when the
// tables cannot be read (not Linux).
func (c *hostPortsCache) get() *nodeapi.HostPorts {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < hostPortsTTL {
		return c.last
	}
	c.at, c.last = time.Now(), readHostPorts()
	return c.last
}

func readHostPorts() *nodeapi.HostPorts {
	read := func(files ...string) (tables []string) {
		for _, f := range files {
			if raw, err := os.ReadFile(f); err == nil {
				tables = append(tables, string(raw))
			}
		}
		return tables
	}
	tcp, udp := read("/proc/net/tcp", "/proc/net/tcp6"), read("/proc/net/udp", "/proc/net/udp6")
	if len(tcp)+len(udp) == 0 {
		return nil
	}
	return parseHostPorts(tcp, udp, ephemeralFrom())
}

// ephemeralFrom is the first port of the kernel's range for outgoing connections.
func ephemeralFrom() int {
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return firstEphemeral
	}
	if f := strings.Fields(string(raw)); len(f) == 2 {
		if lo, err := strconv.Atoi(f[0]); err == nil && lo > 0 {
			return lo
		}
	}
	return firstEphemeral
}

// parseHostPorts reads the text of /proc/net/{tcp,tcp6} and /proc/net/{udp,udp6}: TCP
// sockets in the listen state (0A) and UDP ones that are bound and not connected (07).
// A bound UDP port in the ephemeral range is an outgoing socket's (a DNS query, a proxied
// flow), which comes and goes with traffic and is nobody's to keep: it is left out, so a
// busy node does not report thousands of them.
func parseHostPorts(tcp, udp []string, ephemeral int) *nodeapi.HostPorts {
	return &nodeapi.HostPorts{
		TCP: socketPorts(tcp, "0A", 0),
		UDP: socketPorts(udp, "07", ephemeral),
	}
}

// socketPorts returns the sorted local ports of the sockets in state, from tables in the
// layout of /proc/net: "sl local_address rem_address st ...", addresses in hex. Ports from
// below up are left out; 0: none is.
func socketPorts(tables []string, state string, below int) []int {
	ports := []int{}
	for _, table := range tables {
		for line := range strings.Lines(table) {
			f := strings.Fields(line)
			if len(f) < 4 || f[3] != state {
				continue // the header too
			}
			_, hex, ok := strings.Cut(f[1], ":")
			if !ok {
				continue
			}
			p, err := strconv.ParseUint(hex, 16, 16)
			if err != nil || p == 0 || below > 0 && int(p) >= below {
				continue
			}
			ports = append(ports, int(p))
		}
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}
