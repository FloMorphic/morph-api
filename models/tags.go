package models

import "strings"

// Tag limits. A tag is a label, not a sentence: the caps are there so a
// mis-wired client cannot grow a row without bound.
const (
	MaxTagLen   = 64
	MaxTagCount = 32
)

// NormalizeTags canonicalises a free-form tag list so a tag means the same
// thing whoever typed it: trimmed, lower-cased, inner whitespace collapsed to
// a dash, de-duplicated in first-seen order, empties dropped.
//
// Commas and double quotes are stripped rather than escaped. Tag filtering is
// a membership test over a comma-delimited filter string (see
// repository.TagFilter), and the column holds a JSON array — a tag carrying
// either character would quietly match the wrong rows, so it cannot exist in
// the first place.
//
// A nil input stays nil. That distinction is load-bearing on the way into the
// repository: nil means "the caller said nothing about tags, keep what is
// stored", while an empty non-nil slice means "clear them".
func NormalizeTags(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		t := strings.ToLower(strings.TrimSpace(raw))
		t = strings.Map(func(r rune) rune {
			switch r {
			case ',', '"':
				return -1
			case ' ', '\t', '\n', '\r':
				return '-'
			}
			return r
		}, t)
		for strings.Contains(t, "--") {
			t = strings.ReplaceAll(t, "--", "-")
		}
		t = strings.Trim(t, "-")
		if t == "" || seen[t] {
			continue
		}
		if len(t) > MaxTagLen {
			t = t[:MaxTagLen]
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == MaxTagCount {
			break
		}
	}
	return out
}
