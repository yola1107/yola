// Package header provides Kratos transport headers for long connections.
// It is internal to network protocol packages (tcp/websocket).
package header

import "strings"

// ConnectionIDKey carries the current physical connection ID.
const ConnectionIDKey = "conn_id"

// Carrier is a case-normalized multi-value transport header.
type Carrier map[string][]string

func (c Carrier) Get(key string) string {
	values := c.Values(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (c Carrier) Set(key, value string) {
	c[strings.ToLower(key)] = []string{value}
}

func (c Carrier) Add(key, value string) {
	key = strings.ToLower(key)
	c[key] = append(c[key], value)
}

func (c Carrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}

func (c Carrier) Values(key string) []string {
	return c[strings.ToLower(key)]
}
