package node

import (
	"reflect"
	"testing"
)

const tcpTable = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0805 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 11111 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000   101        0 22222 1 0000000000000000 100 0 0 10 0
   2: 0A00000A:01BB 5DB8D822:C350 01 00000000:00000000 02:000A1B2C 00000000     0        0 33333 2 0000000000000000 21 4 30 10 -1
   3: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 44444 1 0000000000000000 100 0 0 10 0
`

const tcp6Table = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0805 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 55555 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 66666 1 0000000000000000 100 0 0 10 0
   2: 0000000000000000FFFF00000100007F:A1B2 0000000000000000FFFF00000A00000A:01BB 01 00000000:00000000 00:00000000 00000000     0        0 77777 1 0000000000000000 100 0 0 10 0
`

const udpTable = `   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  120: 00000000:01BB 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 88888 2 0000000000000000 0
  121: 0A00000A:9C40 08080808:0035 01 00000000:00000000 00:00000000 00000000     0        0 99999 2 0000000000000000 0
  122: 00000000:C000 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 10101 2 0000000000000000 0
  123: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   101        0 12121 2 0000000000000000 0
`

// A foreign Xray on 2053 (0805) shows up whoever it is; established sockets and ephemeral
// UDP ports do not; both address families merge into one sorted list.
func TestParseHostPorts(t *testing.T) {
	got := parseHostPorts([]string{tcpTable, tcp6Table}, []string{udpTable}, 32768)
	if want := []int{22, 53, 2053, 8080}; !reflect.DeepEqual(got.TCP, want) {
		t.Errorf("tcp: %v, want %v", got.TCP, want)
	}
	if want := []int{53, 443}; !reflect.DeepEqual(got.UDP, want) {
		t.Errorf("udp: %v, want %v", got.UDP, want)
	}
	if !got.Listens("tcp", 2053) || got.Listens("udp", 2053) || !got.Listens("udp", 443) || got.Listens("tcp", 443) {
		t.Errorf("Listens: %+v", got)
	}
}

func TestParseHostPortsEmptyAndBroken(t *testing.T) {
	got := parseHostPorts([]string{"", "garbage\nmore garbage 1 2 3 4\n"}, nil, 32768)
	if got.TCP == nil || len(got.TCP) != 0 || got.UDP == nil || len(got.UDP) != 0 {
		t.Errorf("an unreadable table must give empty lists, not nil: %+v", got)
	}
}
