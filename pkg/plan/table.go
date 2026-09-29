package plan

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"
)

// RenderTable renders entries as a human-readable table grouped by region then resource type,
// with a header per group. It sorts a local copy by reusing Body.Sort rather than
// re-implementing the comparator, so the table's grouping and the JSON body's ordering can never
// drift apart. Every column prints the literal EntryState/scope.RefusalReason string values --
// never a separately maintained display-label map (03-CONTEXT.md's one-vocabulary decision).
//
// DETAIL is Entry.Detail, whatever the API itself said (NR-787). It is a separate column rather
// than folded into REASON because the two are different things: REASON is our own closed
// vocabulary that tooling matches on, DETAIL is free text from OCI, and merging them would make
// the machine-readable field unparseable. Entry.Detail already reached the JSON artifact; both
// surfaces a human watches dropped it -- libnuke's per-round line prints the bare word "failed"
// (pkg/queue/item.go), and this table printed the classification alone, so at best "api-error".
// Diagnosing a deadlocked compartment took half an hour and a separate CLI for a cause the
// process had in hand on its first attempt.
//
// Newlines in Detail are collapsed to spaces: an OCI error_description can be multi-line, and one
// embedded newline would break the tabwriter's column alignment for the whole group.
func RenderTable(entries []Entry) string {
	sorted := Body{Entries: append([]Entry(nil), entries...)}
	sorted.Sort()

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)

	var currentRegion, currentType string
	first := true
	for _, e := range sorted.Entries {
		if e.Region != currentRegion || e.ResourceType != currentType {
			tw.Flush()
			if !first {
				fmt.Fprintln(&buf)
			}
			first = false
			currentRegion, currentType = e.Region, e.ResourceType
			fmt.Fprintf(&buf, "== %s / %s ==\n", currentRegion, currentType)
			fmt.Fprintln(tw, "RESOURCE_ID\tCOMPARTMENT\tSTATE\tREASON\tDETAIL")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			e.ResourceID, e.CompartmentID, string(e.State), string(e.Reason), singleLine(e.Detail))
	}
	tw.Flush()

	return buf.String()
}

// singleLine collapses every run of whitespace to one space so a multi-line API message cannot
// break the tabwriter's alignment for its whole group.
func singleLine(detail string) string {
	return strings.Join(strings.Fields(detail), " ")
}
