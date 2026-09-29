// Package host 从监听地址或 listener 提取可对外发布的 host:port。
// 仅供 network 下的 TCP/WebSocket 协议包使用。
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

// Extract 从监听地址或 listener 提取可对外发布的地址；
// advertiseHost 覆盖推导出的 host，且不得包含端口。
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
		if !ip.IsGlobalUnicast() {
			continue
		}
		if ip.To4() != nil {
			return ip
		}
		fallback = ip
	}
	return fallback
}
