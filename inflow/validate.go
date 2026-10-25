package inflow

import (
	"fmt"
	"sort"
	"strings"

	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// Compile diagnostics — one list of findings a client can point a user at.
//
// The engine's compiler runs its own guard over the lowered graph (the depends
// guard, see the fusion compilers/vueFlow docs): a node that waits for a node
// which can only ever run after it deadlocks the first crossing of the flow, and
// that is a mistake only a graph walk can find. Its findings come back as
// compiler.ValidationIssue values — code, severity, the node and the EDGE to
// re-point, a suggestion and the ids the edge may legally target instead — which
// is exactly what a canvas needs to highlight the mistake.
//
// This file collects those findings together with the ones this compiler owns
// (a node whose builder refused the data it was given, a missing start marker, a
// node nothing connects to) into one Diagnostics value, so every surface — the
// save response, the compile route, the MCP tools, a run launch — reports the
// same shape and nobody has to parse an error string.

// Issue codes added on top of the fusion guard's own (depend_deadlock,
// depend_unreachable, depend_unknown_node, depend_self, depend_without_edge).
const (
	// CodeStartNodeMissing: the flow has no `startNode`, so there is nothing to
	// compile from. Shares the fusion guard's code — it is the same finding.
	CodeStartNodeMissing = compiler.CodeStartNodeMissing
	// CodeNodeBuildFailed: a node's builder refused the data the node carries
	// (a plugin node with no action, a node whose data is not an object). The
	// walk stops at such a node, so the graph past it is not lowered either.
	CodeNodeBuildFailed compiler.ValidationCode = "node_build_failed"
	// CodePluginAccountUnresolved: a plugin-backed node could not be lowered
	// because its infra account could not be minted — the inflow runtime is not
	// connected (yet). An environment problem, not a graph mistake, and it is
	// reported as its own code so a client can say so instead of blaming the
	// author. Still an error: the node does not compile, so the run cannot work.
	CodePluginAccountUnresolved compiler.ValidationCode = "plugin_account_unresolved"
	// CodeNodeUnreachable: a node of the graph the walk never reaches from the
	// start node. It compiles, it just never runs — a warning, because a staging
	// area of nodes an author is still wiring up is a normal state to save in.
	CodeNodeUnreachable compiler.ValidationCode = "node_unreachable"
)

// Diagnostics is the outcome of a compile pass, ready to be put on an API
// response: every finding, with error / warning counts and an `ok` flag so a
// client can branch without walking the list.
type Diagnostics struct {
	// OK is false when at least one error-severity issue was found.
	OK bool `json:"ok"`
	// StartNodeID is the node the walk started from, empty when none was found.
	StartNodeID string `json:"startNodeId,omitempty"`
	// Issues is every finding, errors first, then warnings — the order a list
	// should be rendered in. Never nil, so a JSON consumer can iterate it.
	Issues []compiler.ValidationIssue `json:"issues"`
	// ErrorCount / WarningCount count Issues by severity.
	ErrorCount   int `json:"errorCount"`
	WarningCount int `json:"warningCount"`
}

// HasErrors reports whether anything error-severity was found.
func (d Diagnostics) HasErrors() bool { return d.ErrorCount > 0 }

// IsGuardCode reports whether a code is a whole-graph VERDICT — something the
// guard concluded by walking from the start node — as opposed to a fact about
// the lowering itself (a node that did not compile).
//
// The two are reported together but mean different things, and only the second
// one may ever change what a caller does: the graph it asked for is incomplete.
// A guard finding is advice about a traversal, so it is shown, never enforced.
func IsGuardCode(c compiler.ValidationCode) bool {
	switch c {
	case compiler.CodeDependDeadlock, compiler.CodeDependUnreachable,
		compiler.CodeDependUnknownNode, compiler.CodeDependSelf,
		compiler.CodeDependWithoutEdge, CodeNodeUnreachable:
		return true
	}
	return false
}

// CompileErr is the subset of Err that means **the graph did not lower**: a
// node whose builder refused its data, a missing start marker. Guard verdicts
// are left out, so a caller can keep its "did this compile?" answer exactly as
// it was before the guard existed while still showing the guard's suggestions.
func (d Diagnostics) CompileErr() error {
	msgs := []string{}
	for _, i := range d.Errors() {
		if IsGuardCode(i.Code) {
			continue
		}
		msgs = append(msgs, issueText(i))
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

// Errors returns the error-severity issues only.
func (d Diagnostics) Errors() []compiler.ValidationIssue {
	return d.bySeverity(compiler.SeverityError)
}

// Warnings returns the warning-severity issues only.
func (d Diagnostics) Warnings() []compiler.ValidationIssue {
	return d.bySeverity(compiler.SeverityWarning)
}

func (d Diagnostics) bySeverity(s compiler.ValidationSeverity) []compiler.ValidationIssue {
	out := []compiler.ValidationIssue{}
	for _, i := range d.Issues {
		if i.Severity == s {
			out = append(out, i)
		}
	}
	return out
}

// Err collapses the error-severity issues into one error, nil when there are
// none. It is what keeps the old `(start, nodes, error)` compile contract
// working for callers that only want to refuse a flow.
func (d Diagnostics) Err() error {
	errs := d.Errors()
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, issueText(e))
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

// Summary is a one-line human reading of the outcome, for a log line or a
// toast: "2 errors, 1 warning" style, or the single issue when there is one.
func (d Diagnostics) Summary() string {
	switch {
	case d.ErrorCount == 0 && d.WarningCount == 0:
		return "no issues"
	case d.ErrorCount == 1 && d.WarningCount == 0:
		return issueText(d.Errors()[0])
	case d.ErrorCount == 0 && d.WarningCount == 1:
		return issueText(d.Warnings()[0])
	}
	return fmt.Sprintf("%s, %s", plural(d.ErrorCount, "error"), plural(d.WarningCount, "warning"))
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// issueText is one issue as a sentence: its own Error() text, prefixed with the
// node it sits on so a log line names the node without the reader opening the
// canvas.
func issueText(i compiler.ValidationIssue) string {
	if i.NodeId == "" {
		return i.Error()
	}
	return fmt.Sprintf("node %s: %s", i.NodeId, i.Error())
}

// add appends issues and keeps the counts and ordering of the Diagnostics
// correct. Errors sort before warnings, each group keeping insertion order
// (which is node-id order, see CompileFlow) so the list is stable between two
// compiles of the same graph — a client diffing it must not see churn.
func (d *Diagnostics) add(issues ...compiler.ValidationIssue) {
	d.Issues = append(d.Issues, issues...)
	sort.SliceStable(d.Issues, func(a, b int) bool {
		return d.Issues[a].Severity == compiler.SeverityError &&
			d.Issues[b].Severity != compiler.SeverityError
	})
	d.ErrorCount, d.WarningCount = 0, 0
	for _, i := range d.Issues {
		if i.Severity == compiler.SeverityError {
			d.ErrorCount++
			continue
		}
		d.WarningCount++
	}
	d.OK = d.ErrorCount == 0
}
