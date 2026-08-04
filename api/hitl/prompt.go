package hitlControllers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/FloMorphic/morph-api/models"
)

// The variable syntax a HITL prompt is written in: `{{$.some.path}}`, the same
// dialect the node drawers use everywhere else on the canvas.
var promptVarRE = regexp.MustCompile(`\{\{\s*(\$[^}\s]*)\s*\}\}`)

// resolveSession fills in everything the task could not resolve when it was
// recorded.
//
// The hitl svc handler runs outside the flow's expression scope, so it stores
// the prompt as a template and the refs as bare paths. The moment a session
// actually starts — a person opening the task — is the moment those have to
// become text, and this is where that happens. Resolution is against the Data
// snapshot the node captured (never a re-read of the live context): the person
// must see the run as it was when it reached the node, not as it is now.
//
// Nothing is persisted. PromptResolved and each Ref.Value are per-read fields;
// the durable facts stay the template plus the snapshot.
func resolveSession(t *models.HumanTask) {
	if t == nil {
		return
	}
	if t.Prompt != "" {
		t.PromptResolved = resolvePrompt(t.Prompt, t.Data)
	}
	for i := range t.Refs {
		if v, ok := lookupPath(t.Data, t.Refs[i].Path); ok {
			t.Refs[i].Value = v
		}
	}
}

// resolvePrompt substitutes every `{{$.path}}` in the template with its value
// from data. A path that does not resolve is left as written — showing the
// person the unfilled variable is more honest than silently dropping the
// subject the prompt was built around.
func resolvePrompt(template string, data map[string]any) string {
	return promptVarRE.ReplaceAllStringFunc(template, func(match string) string {
		groups := promptVarRE.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		v, ok := lookupPath(data, groups[1])
		if !ok {
			return match
		}
		return renderValue(v)
	})
}

// lookupPath walks a `$.a.b[0].c` path through a decoded JSON object. It is
// deliberately not a full JSONPath engine — no filters, no wildcards, no
// recursive descent: a prompt variable names one value, and anything more
// expressive belongs in the flow that built the data, not in the sentence shown
// to a person.
func lookupPath(data map[string]any, path string) (any, bool) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || data == nil {
		return nil, false
	}
	trimmed = strings.TrimPrefix(trimmed, "$")
	trimmed = strings.TrimPrefix(trimmed, ".")
	// `$` alone means the whole snapshot.
	if trimmed == "" {
		return data, true
	}

	var current any = data
	for _, segment := range strings.Split(trimmed, ".") {
		name, indexes := splitIndexes(segment)
		if name != "" {
			obj, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			current, ok = obj[name]
			if !ok {
				return nil, false
			}
		}
		for _, idx := range indexes {
			arr, ok := current.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			current = arr[idx]
		}
	}
	return current, true
}

// splitIndexes pulls the bracket subscripts off one path segment:
// `items[0][2]` → ("items", [0, 2]). A non-numeric subscript makes the whole
// segment unresolvable, which lookupPath reports as a miss.
func splitIndexes(segment string) (string, []int) {
	open := strings.Index(segment, "[")
	if open < 0 {
		return segment, nil
	}
	name := segment[:open]
	var indexes []int
	for _, raw := range strings.Split(segment[open:], "[") {
		raw = strings.TrimSuffix(strings.TrimSpace(raw), "]")
		if raw == "" {
			continue
		}
		i, err := strconv.Atoi(raw)
		if err != nil {
			return name, []int{-1}
		}
		indexes = append(indexes, i)
	}
	return name, indexes
}

// renderValue turns a resolved value into prompt text: strings inline as-is,
// scalars via their natural formatting, and anything structured (the message
// stack an LLM/MCP node built, a tool result) as indented JSON — which is what
// makes it readable in the conversation rather than a Go-syntax dump.
func renderValue(v any) string {
	switch typed := v.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	}
	if b, err := json.MarshalIndent(v, "", "  "); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}
