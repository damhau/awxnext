// Package jsonpath evaluates the simple JSONPaths ($.a.b[0]['c']) that
// webhook mappings and event stream mappings use.
package jsonpath

import (
	"regexp"
	"strconv"
)

var pathRe = regexp.MustCompile(`^\$((\.[A-Za-z0-9_-]+)|(\[\d+\])|(\['[^']*'\])|(\["[^"]*"\]))*$`)
var segRe = regexp.MustCompile(`\.([A-Za-z0-9_-]+)|\[(\d+)\]|\['([^']*)'\]|\["([^"]*)"\]`)

// Valid reports whether path is a JSONPath Get understands.
func Valid(path string) bool { return pathRe.MatchString(path) }

// Get evaluates path against a decoded JSON document.
func Get(doc any, path string) (any, bool) {
	if !Valid(path) {
		return nil, false
	}
	cur := doc
	for _, m := range segRe.FindAllStringSubmatch(path[1:], -1) {
		switch {
		case m[2] != "":
			list, ok := cur.([]any)
			i, _ := strconv.Atoi(m[2])
			if !ok || i >= len(list) {
				return nil, false
			}
			cur = list[i]
		default:
			key := m[1] + m[3] + m[4]
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = obj[key]; !ok {
				return nil, false
			}
		}
	}
	return cur, true
}
