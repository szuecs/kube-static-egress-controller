package main

import (
	"fmt"
	"strings"
)

// StringMap is a map of key value pairs.
type StringMap map[string]string

func (s StringMap) String() string {
	elements := make([]string, 0, len(s))
	for k, v := range s {
		elements = append(elements, fmt.Sprintf("%s=%s", k, v))
	}

	return strings.Join(elements, ",")
}

// Set parses a string of the format: `key=value`.
func (s StringMap) Set(value string) error {
	if s == nil {
		s = StringMap(map[string]string{})
	}

	kv := strings.Split(value, "=")
	if len(kv) != 2 {
		return fmt.Errorf("invalid key=value format: %s", value)
	}

	s[kv[0]] = kv[1]

	return nil
}

// IsCumulative always return true because it's allowed to call Set multiple
// times.
func (_ StringMap) IsCumulative() bool {
	return true
}
