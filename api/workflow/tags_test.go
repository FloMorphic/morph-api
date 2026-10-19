package workflowControllers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// A flow's tags are what another system narrows this install's flow list by
// (SurfStat's "run a flow on this row" picker shows one framework's flows
// instead of every flow on the canvas), so the three things under test are the
// list filter, the relabel route, and an import that stamps its own labels.

// minimalDoc is the smallest importable document: one start node.
const minimalDoc = `{"flomorphic":{"kind":"workflow","version":1},"title":"Imported","nodes":[{"ref":"s","kind":"startNode"}]}`

func listFlowTitles(t *testing.T, app *fiber.App, query string) []string {
	t.Helper()
	res, err := app.Test(httptest.NewRequest("GET", "/flow"+query, nil), fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("GET /flow%s = %d", query, res.StatusCode)
	}
	var out struct {
		Data struct {
			List  []models.FlowRecord `json:"list"`
			Total int64               `json:"total"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if int(out.Data.Total) != len(out.Data.List) {
		t.Fatalf("GET /flow%s: total %d but %d rows", query, out.Data.Total, len(out.Data.List))
	}
	titles := make([]string, 0, len(out.Data.List))
	for _, f := range out.Data.List {
		titles = append(titles, f.Title)
	}
	sort.Strings(titles)
	return titles
}

func TestListFlowsFilteredByTags(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()
	ctx := context.Background()
	for _, f := range []models.FlowRecord{
		{Title: "fraud sweep", Tags: []string{"framework:fraud", "fraud"}},
		{Title: "case review", Tags: []string{"framework:fraud", "on-row"}},
		{Title: "hand-drawn", Tags: nil},
	} {
		rec := f
		if err := store.Workflows().Upsert(ctx, &rec); err != nil {
			t.Fatal(err)
		}
	}

	// No filter is every flow — what the picker shows until someone narrows it,
	// and what it shows again the moment they clear the filter.
	if got := listFlowTitles(t, app, ""); len(got) != 3 {
		t.Fatalf("unfiltered = %v, want 3", got)
	}
	if got := strings.Join(listFlowTitles(t, app, "?tags=framework:fraud"), "|"); got != "case review|fraud sweep" {
		t.Fatalf("framework filter = %q", got)
	}
	if got := strings.Join(listFlowTitles(t, app, "?tags=on-row"), "|"); got != "case review" {
		t.Fatalf("label filter = %q", got)
	}
	// Match ANY across several labels, and a loosely typed filter still hits.
	if got := len(listFlowTitles(t, app, "?tags=on-row,fraud")); got != 2 {
		t.Fatalf("multi-label filter = %d rows, want 2", got)
	}
	if got := len(listFlowTitles(t, app, "?tags=%20Framework%3AFraud%20")); got != 2 {
		t.Fatalf("loose filter = %d rows, want 2", got)
	}
	// An empty parameter is not a filter that matches nothing.
	if got := len(listFlowTitles(t, app, "?tags=")); got != 3 {
		t.Fatalf("empty tags = %d rows, want 3", got)
	}
	if got := len(listFlowTitles(t, app, "?tags=nothing-carries-this")); got != 0 {
		t.Fatalf("unknown label matched %d rows", got)
	}
}

func putTags(t *testing.T, app *fiber.App, id string, body map[string]any) (int, []string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/flow/id/"+id+"/tags", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var out struct {
		Data struct {
			Tags []string `json:"tags"`
		} `json:"data"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out.Data.Tags
}

func TestSetFlowTags(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()
	ctx := context.Background()
	rec := models.FlowRecord{Title: "drawn by hand", Tags: []string{"mine"}}
	if err := store.Workflows().Upsert(ctx, &rec); err != nil {
		t.Fatal(err)
	}

	// merge keeps what the author put there — an installer claiming a flow for
	// its framework has no business dropping the operator's own labels.
	code, tags := putTags(t, app, rec.ID, map[string]any{"tags": []string{"framework:gdpr"}, "mode": "merge"})
	if code != 200 || strings.Join(tags, "|") != "mine|framework:gdpr" {
		t.Fatalf("merge = %d %v", code, tags)
	}
	// The graph survives a relabel: the whole point of the route is not having
	// to resend it.
	after, err := store.Workflows().GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "drawn by hand" {
		t.Fatalf("relabel changed the flow: %+v", after)
	}

	// set replaces, and an empty list clears.
	if code, tags := putTags(t, app, rec.ID, map[string]any{"tags": []string{"only-this"}}); code != 200 || strings.Join(tags, "|") != "only-this" {
		t.Fatalf("set = %d %v", code, tags)
	}
	if code, tags := putTags(t, app, rec.ID, map[string]any{"tags": []string{}}); code != 200 || len(tags) != 0 {
		t.Fatalf("clear = %d %v", code, tags)
	}
	if code, _ := putTags(t, app, rec.ID, map[string]any{"tags": []string{"x"}, "mode": "sideways"}); code != 400 {
		t.Fatalf("bad mode = %d, want 400", code)
	}
	if code, _ := putTags(t, app, "flow_nope", map[string]any{"tags": []string{"x"}}); code != 404 {
		t.Fatalf("missing flow = %d, want 404", code)
	}
}

func TestImportStampsTags(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()

	// The importer's labels win over the document's: a package installer knows
	// which framework the flow belongs to on THIS install.
	out := postImport(t, app, map[string]any{
		"workflow": json.RawMessage(minimalDoc),
		"tags":     []string{"framework:gdpr", "GDPR"},
	})
	id := flowIDOf(t, out)
	rec, err := store.Workflows().GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rec.Tags, "|"); got != "framework:gdpr|gdpr" {
		t.Fatalf("imported tags = %q", got)
	}

	// A re-install that says nothing about tags keeps them — an older
	// installer must not strip the labels the picker filters by.
	postImport(t, app, map[string]any{"workflow": json.RawMessage(minimalDoc), "id": id})
	rec, err = store.Workflows().GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rec.Tags, "|"); got != "framework:gdpr|gdpr" {
		t.Fatalf("tags lost on a tag-less re-import = %q", got)
	}

	// A document that carries its own labels is imported with them.
	doc := `{"flomorphic":{"kind":"workflow","version":1},"title":"From a file","tags":["from-the-file"],"nodes":[{"ref":"s","kind":"startNode"}]}`
	out = postImport(t, app, map[string]any{"workflow": json.RawMessage(doc)})
	rec, err = store.Workflows().GetByID(context.Background(), flowIDOf(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rec.Tags, "|"); got != "from-the-file" {
		t.Fatalf("document tags = %q", got)
	}
}

// flowIDOf reads the saved flow's id out of an import response.
func flowIDOf(t *testing.T, out map[string]any) string {
	t.Helper()
	data, _ := out["data"].(map[string]any)
	flow, _ := data["flow"].(map[string]any)
	id, _ := flow["id"].(string)
	if id == "" {
		t.Fatalf("no flow id in the import response: %v", out)
	}
	return id
}
