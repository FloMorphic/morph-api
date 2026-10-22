package models

import (
	"errors"
	"fmt"
	"strings"
)

// Writing one location of a context document.
//
// A context document is a JSON object shared by everything a run touches, so
// seeding data into it must be surgical: it updates the ONE location a path
// names, creates only the objects that path needs on the way down, and leaves
// every other key — including whatever an earlier node or run left there —
// exactly as it was. Nothing here ever replaces a document or starts a blank
// one.
//
// That is also why the accepted path language is deliberately plain:
//
//	$                  the document root
//	$._entry           a member (the default entry target)
//	$.input.order      nested members, created if they are missing
//	$['order id']      a member whose name is not an identifier
//
// Array, wildcard, filter and descendant paths (`$.items[0]`, `$.items[*]`,
// `$..id`, `$.items[?(@.ok)]`) are rejected: each selects zero or many
// locations, and a write needs exactly one.

// ParseContextPath splits a write path into the member names it walks. The root
// (`$`, or an empty string) yields no members. A `$` prefix is optional, so
// both `$._entry` and `_entry` parse.
func ParseContextPath(path string) ([]string, error) {
	p := strings.TrimSpace(path)
	if p == "" || p == "$" {
		return nil, nil
	}
	p = strings.TrimPrefix(p, "$")
	if strings.Contains(p, "..") || strings.ContainsAny(p, "*?@") {
		return nil, fmt.Errorf("context path %q selects more than one location; a write target must name exactly one member path (e.g. $.input.order)", path)
	}

	var out []string
	for p != "" {
		if p[0] == '[' {
			end := strings.IndexByte(p, ']')
			if end < 0 {
				return nil, fmt.Errorf("context path %q has an unclosed `[`", path)
			}
			raw := strings.TrimSpace(p[1:end])
			if len(raw) < 3 || (raw[0] != '\'' && raw[0] != '"') || raw[len(raw)-1] != raw[0] {
				return nil, fmt.Errorf("context path %q indexes %q, which is not an object member; bracket a quoted name instead (e.g. $['order id'])", path, raw)
			}
			out = append(out, raw[1:len(raw)-1])
			p = p[end+1:]
			continue
		}
		if p[0] == '.' {
			p = p[1:]
			if p == "" {
				return nil, fmt.Errorf("context path %q ends in a dot", path)
			}
			if p[0] == '[' {
				continue
			}
		}
		name := p
		if i := strings.IndexAny(p, ".["); i >= 0 {
			name, p = p[:i], p[i:]
		} else {
			p = ""
		}
		if name == "" {
			return nil, fmt.Errorf("context path %q has an empty member name", path)
		}
		out = append(out, name)
	}
	return out, nil
}

// SetAtContextPath writes value into doc at path, creating the intermediate
// objects the path names and touching nothing else.
//
// At the root an object value is merged key by key — the document keeps every
// key the value does not name — because replacing a context document wholesale
// is exactly the clobber this is here to avoid. Deeper down, the member the
// path ends at is replaced: that slot is the value's own.
//
// An intermediate that exists and is not an object is reported, never
// overwritten: `$.a.b` over `{"a": 42}` is an error telling the caller it would
// have to throw the 42 away.
func SetAtContextPath(doc map[string]any, path string, value any) error {
	if doc == nil {
		return errors.New("there is no context document to write into")
	}
	members, err := ParseContextPath(path)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("a context document is a JSON object, so the root path %q cannot be given a %s", path, jsonKind(value))
		}
		for k, v := range obj {
			doc[k] = v
		}
		return nil
	}

	cur := doc
	for i, m := range members[:len(members)-1] {
		switch held := cur[m].(type) {
		case map[string]any:
			cur = held
		case nil:
			child := map[string]any{}
			cur[m] = child
			cur = child
		default:
			return fmt.Errorf("context path %q runs through %s, which holds a %s; the write would have to overwrite it", path, renderContextPath(members[:i+1]), jsonKind(held))
		}
	}
	cur[members[len(members)-1]] = value
	return nil
}

// ValueAtContextPath reads the value at path, reporting whether the document
// holds anything there. The root yields the document itself. A path that runs
// through a non-object reads as absent rather than as an error — a reader only
// wants to know whether there is something to show.
func ValueAtContextPath(doc map[string]any, path string) (any, bool) {
	members, err := ParseContextPath(path)
	if err != nil || doc == nil {
		return nil, false
	}
	if len(members) == 0 {
		return doc, true
	}
	var cur any = doc
	for _, m := range members {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[m]
		if !ok {
			return nil, false
		}
	}
	return cur, cur != nil
}

// renderContextPath writes member names back as a path, for error messages
// that point at the segment that stopped the write.
func renderContextPath(members []string) string {
	var b strings.Builder
	b.WriteString("$")
	for _, m := range members {
		if isPlainMember(m) {
			b.WriteString(".")
			b.WriteString(m)
			continue
		}
		b.WriteString("['")
		b.WriteString(m)
		b.WriteString("']")
	}
	return b.String()
}

// isPlainMember reports whether a name can be written after a dot.
func isPlainMember(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// jsonKind names a value's JSON type for an error message — a reader needs to
// know what is in the way, not its Go type.
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		return "number"
	}
}
