package loom

import (
	"regexp"
	"sort"
)

var referencePattern = regexp.MustCompile(`loom://[0-9a-fA-F]{32}`)

// ExtractReferences returns the distinct valid Loom references embedded in
// arbitrary text values.
func ExtractReferences(values ...string) []Ref {
	seen := make(map[Ref]struct{})
	for _, value := range values {
		for _, match := range referencePattern.FindAllString(value, -1) {
			ref, err := ParseRef(match)
			if err == nil {
				seen[ref] = struct{}{}
			}
		}
	}
	result := make([]Ref, 0, len(seen))
	for ref := range seen {
		result = append(result, ref)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
