package host

import (
	"net"
	"strings"
	"testing"
)

type testListener struct{ address *net.TCPAddr }

func (l testListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (testListener) Close() error                { return nil }
func (l testListener) Addr() net.Addr            { return l.address }

func TestExtractHostPriorityAndIPv6(t *testing.T) {
	listener := testListener{address: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3101}}
	tests := []struct {
		name          string
		listenAddress string
		advertiseHost string
		want          string
	}{
		{name: "advertise overrides listen", listenAddress: "127.0.0.1:0", advertiseHost: "game.example.com", want: "game.example.com:3101"},
		{name: "bare IPv6", listenAddress: "127.0.0.1:0", advertiseHost: "2001:db8::1", want: "[2001:db8::1]:3101"},
		{name: "bracketed IPv6", listenAddress: "127.0.0.1:0", advertiseHost: "[2001:db8::1]", want: "[2001:db8::1]:3101"},
		{name: "IPv6 zone", listenAddress: "127.0.0.1:0", advertiseHost: "fe80::1%eth0", want: "[fe80::1%eth0]:3101"},
		{name: "listen host", listenAddress: "192.0.2.1:0", want: "192.0.2.1:3101"},
		{name: "listener loopback", listenAddress: ":0", want: "127.0.0.1:3101"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Extract(test.listenAddress, test.advertiseHost, listener)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Extract() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExtractRejectsInvalidAdvertiseHost(t *testing.T) {
	listener := testListener{address: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3101}}
	for _, advertiseHost := range []string{
		"0.0.0.0",
		"::",
		"host.example.com:3101",
		" host.example.com",
		"host name",
		"host/example.com",
		"user@host.example.com",
		"host.example.com?query",
		"host.example.com#fragment",
		"[host.example.com]",
		"foo%bar",
		"foo\u00a0bar",
		"foo\x7fbar",
	} {
		t.Run(advertiseHost, func(t *testing.T) {
			_, err := Extract(":0", advertiseHost, listener)
			if err == nil || !strings.Contains(err.Error(), "advertise host") {
				t.Fatalf("Extract() error = %v, want advertise host error", err)
			}
		})
	}
}

func TestSelectInterfaceIP(t *testing.T) {
	tests := []struct {
		name  string
		addrs []net.Addr
		want  net.IP
	}{
		{name: "no address"},
		{
			name: "ignore non-global address",
			addrs: []net.Addr{
				&net.IPNet{IP: net.ParseIP("127.0.0.1")},
				&net.IPAddr{IP: net.ParseIP("fe80::1")},
			},
		},
		{
			name: "last IPv6 fallback",
			addrs: []net.Addr{
				&net.IPAddr{IP: net.ParseIP("2001:db8::1")},
				&net.IPNet{IP: net.ParseIP("2001:db8::2")},
			},
			want: net.ParseIP("2001:db8::2"),
		},
		{
			name: "first IPv4 preferred",
			addrs: []net.Addr{
				&net.IPAddr{IP: net.ParseIP("2001:db8::1")},
				&net.IPNet{IP: net.ParseIP("192.0.2.1")},
				&net.IPAddr{IP: net.ParseIP("198.51.100.1")},
			},
			want: net.ParseIP("192.0.2.1"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := selectInterfaceIP(test.addrs); !got.Equal(test.want) {
				t.Fatalf("selectInterfaceIP() = %v, want %v", got, test.want)
			}
		})
	}
}
