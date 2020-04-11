package cfsync

import "strings"

type StringSet map[string]struct{}

func NewStringSet(name ...string) StringSet {
	s := make(StringSet)
	s.AddAll(name...)
	return s
}

func (s StringSet) String() string {
	var builder strings.Builder
	builder.WriteString("[")
	first := true
	for k, _ := range s {
		if !first {
			builder.WriteString(",")
		} else {
			first = false
		}
		builder.WriteString(k)
	}
	builder.WriteString("]")
	return builder.String()
}

func (s StringSet) Add(name string) {
	s[name] = struct{}{}
}

func (s StringSet) AddAll(name ...string) {
	for _, k := range name {
		s.Add(k)
	}
}

func (s StringSet) Contains(name string) bool {
	_, exists := s[name]
	return exists
}

func (s StringSet) Difference(other StringSet) StringSet {
	result := NewStringSet()
	for k, _ := range s {
		if !other.Contains(k) {
			result.Add(k)
		}
	}
	return result
}
