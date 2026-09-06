// Package host extracts a publishable host:port from a listen address or listener.
// It is internal to network protocol packages (tcp/websocket).
package host

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
)

// Extract returns an address suitable for publishing from a listen address or
// listener. advertiseHost overrides every inferred host and must not include a port.
func Extract(hostPort, advertiseHost string, lis net.Listener) (string, error) {
	listenHost, port, err := net.SplitHostPort(hostPort)
	if err != nil && lis == nil {
		return "", err
	}
	if lis != nil {
		listenerAddr, ok := lis.Addr().(*net.TCPAddr)
		if !ok {
			return "", fmt.Errorf("failed to extract port: %v", lis.Addr())
		}
		port = strconv.Itoa(listenerAddr.Port)
		if advertiseHost == "" && isUnspecified(listenHost) && len(listenerAddr.IP) > 0 && !listenerAddr.IP.IsUnspecified() {
			listenHost = listenerAddr.IP.String()
		}
	}
	if advertiseHost != "" {
		host, err := normalizeAdvertiseHost(advertiseHost)
		if err != nil {
			return "", err
		}
		return net.JoinHostPort(host, port), nil
	}
	if !isUnspecified(listenHost) {
		return net.JoinHostPort(listenHost, port), nil
	}
	return extractInterfaceAddress(port)
}

func normalizeAdvertiseHost(host string) (string, error) {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if _, err := netip.ParseAddr(host); err != nil {
			return "", fmt.Errorf("invalid advertise host %q", host)
		}
	}
	if host == "" {
		return "", errors.New("advertise host is empty")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.IsUnspecified() {
			return "", errors.New("advertise host is unspecified")
		}
		return host, nil
	}
	if strings.Contains(host, ":") {
		return "", fmt.Errorf("invalid advertise host %q", host)
	}
	if strings.ContainsAny(host, "/@?#[]\\%") || strings.IndexFunc(host, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return "", fmt.Errorf("invalid advertise host %q", host)
	}
	return host, nil
}

func isUnspecified(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	return ip != nil && ip.IsUnspecified()
}

func extractInterfaceAddress(port string) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	selectedIndex := int(^uint(0) >> 1)
	var selectedIP net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || selectedIP != nil && iface.Index >= selectedIndex {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		ip := selectInterfaceIP(addrs)
		if ip == nil {
			continue
		}
		selectedIndex = iface.Index
		selectedIP = ip
	}
	if selectedIP == nil {
		return "", errors.New("no global-unicast interface address found")
	}
	return net.JoinHostPort(selectedIP.String(), port), nil
}

func selectInterfaceIP(addrs []net.Addr) net.IP {
	var fallback net.IP
	for _, rawAddr := range addrs {
		var ip net.IP
		switch addr := rawAddr.(type) {
		case *net.IPAddr:
			ip = addr.IP
		case *net.IPNet:
			ip = addr.IP
		}
		if !validIP(ip) {
			continue
		}
		if ip.To4() != nil {
			return ip
		}
		fallback = ip
	}
	return fallback
}

func validIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsInterfaceLocalMulticast()
}
