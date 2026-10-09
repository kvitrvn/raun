package web

import (
	"fmt"
	"net/url"
	"slices"
	"testing"

	"github.com/kvitrvn/raun/internal/knowledge"
)

func TestListFilters(t *testing.T) {
	persona := fixture(t, "persona")
	requirement := fixture(t, "requirement")
	rejected := fixture(t, "persona")
	rejected.ID = "persona-aaaaaa"
	rejected.Status = knowledge.StatusRejected
	validated := fixture(t, "persona")
	validated.ID = "persona-bbbbbb"
	validated.Status = knowledge.StatusValidated
	items := []*knowledge.Item{requirement, rejected, persona, validated}
	for _, tt := range []struct {
		query string
		ids   []string
	}{
		{"", []string{persona.ID, requirement.ID}},
		{"status=pending", []string{persona.ID, requirement.ID}},
		{"status=all", []string{validated.ID, persona.ID, rejected.ID, requirement.ID}},
		{"status=rejected", []string{rejected.ID}},
		{"status=validated", []string{validated.ID}},
		{"status=contested", []string{requirement.ID}},
		{"type=requirement", []string{requirement.ID}},
		{"q=TENANT", []string{persona.ID}},
		{"q=HEALTHY", []string{persona.ID}},
		{"q=Change+billing", []string{persona.ID}},
		{"q=Legal", []string{requirement.ID}},
		{"q=business-rule", []string{requirement.ID}},
		{"q=8FZ2MC", []string{requirement.ID}},
		{"q=PLATFORM", []string{persona.ID}},
		{"type=persona&status=contested", nil},
		{"q=beta", nil}, // Agent metadata is not business content.
	} {
		t.Run(tt.query, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			f, err := parseFilters(q)
			if err != nil {
				t.Fatal(err)
			}
			m := listModel(items, f)
			var ids []string
			for _, it := range m.Items {
				ids = append(ids, it.ID)
			}
			if !slices.Equal(ids, tt.ids) {
				t.Errorf("IDs = %v, want %v", ids, tt.ids)
			}
			if m.Counts.Total != 4 || len(m.Palette) != 4 {
				t.Errorf("workspace counts = %+v, palette %d", m.Counts, len(m.Palette))
			}
		})
	}
	if items[0] != requirement {
		t.Fatal("list changed input order")
	}
}

func TestCounts(t *testing.T) {
	var items []*knowledge.Item
	add := func(n int, status knowledge.Status, typ knowledge.Type, disagreement bool) {
		for range n {
			it := &knowledge.Item{ID: fmt.Sprintf("%s-%06d", typ, len(items)), Type: typ, Status: status}
			if disagreement {
				it.OpenPoints = []knowledge.OpenPoint{{ID: "p1", Kind: knowledge.PointDisagreement}}
			}
			items = append(items, it)
		}
	}
	add(2, knowledge.StatusValidated, knowledge.TypePersona, false)
	add(1, knowledge.StatusNeedsReview, knowledge.TypePersona, false)
	add(1, knowledge.StatusContested, knowledge.TypeRequirement, true)
	add(4, knowledge.StatusProposed, knowledge.TypeRequirement, false)
	add(1, knowledge.StatusRejected, knowledge.TypeRequirement, false)
	c := countItems(items)
	if c.Total != 9 || c.Personas != 3 || c.Requirements != 6 || c.Pending != 6 || c.Decided != 3 || c.Blocked != 1 {
		t.Fatalf("counts = %+v", c)
	}
	var grow []int
	for _, s := range c.Statuses {
		grow = append(grow, s.Grow)
	}
	if !slices.Equal(grow, []int{2, 1, 1, 4, 1}) {
		t.Fatalf("grow = %v", grow)
	}
	for _, tt := range []struct{ n, total, want int }{
		{0, 10, 0}, {3, 10, 3}, {100, 100, 100}, {1, 1000, 1}, {333, 1000, 33}, {500, 1000, 50}, {1000, 1000, 100},
	} {
		if got := growFactor(tt.n, tt.total); got != tt.want {
			t.Errorf("growFactor(%d, %d) = %d, want %d", tt.n, tt.total, got, tt.want)
		}
	}
}

func TestPagination(t *testing.T) {
	var items []*knowledge.Item
	for i := 102; i >= 1; i-- {
		items = append(items, &knowledge.Item{ID: fmt.Sprintf("persona-%06d", i), Type: knowledge.TypePersona, Status: knowledge.StatusProposed, Persona: &knowledge.Persona{}})
	}
	for _, tt := range []struct {
		page, count int
		first       string
		actual      int
	}{
		{1, 50, "persona-000001", 1}, {2, 50, "persona-000051", 2}, {3, 2, "persona-000101", 3}, {int(^uint(0) >> 1), 2, "persona-000101", 3},
	} {
		m := listModel(items, filters{Page: tt.page})
		if len(m.Items) != tt.count || m.Items[0].ID != tt.first || m.Filters.Page != tt.actual || m.Pages != 3 || m.Total != 102 {
			t.Fatalf("pagination: %+v", m)
		}
	}
	f := filters{Query: "a & b", Type: "persona", Status: "all", Page: 2}
	for _, id := range []string{"", "persona-000007"} {
		u, err := url.Parse(f.pageURL(id, 3))
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseFilters(u.Query())
		if err != nil {
			t.Fatal(err)
		}
		if want := f.withPage(3); got != want {
			t.Fatalf("lost filters: %+v", got)
		}
		if want := "/knowledge"; id != "" {
			want = itemURL(id)
			if u.Path != want {
				t.Fatalf("path = %q", u.Path)
			}
		} else if u.Path != want {
			t.Fatalf("path = %q", u.Path)
		}
	}
}

func TestFilterURLs(t *testing.T) {
	f := filters{Query: "invoice", Type: "requirement", Status: statusPending, Page: 2}
	for _, tt := range []struct {
		got, want string
	}{
		{filters{Status: statusPending, Page: 1}.url(""), "/knowledge"},
		{f.url("requirement-8fz2mc"), "/knowledge/requirement-8fz2mc?page=2&q=invoice&type=requirement"},
		// Changing a filter keeps the shown item, the other filters, and returns to page 1.
		{f.withStatus("validated").url("requirement-8fz2mc"), "/knowledge/requirement-8fz2mc?q=invoice&status=validated&type=requirement"},
		{f.withType("").url("requirement-8fz2mc"), "/knowledge/requirement-8fz2mc?q=invoice"},
	} {
		if tt.got != tt.want {
			t.Errorf("url = %q, want %q", tt.got, tt.want)
		}
	}
}

func TestInvalidFilters(t *testing.T) {
	for _, query := range []string{"type=unknown", "status=unknown", "status=decided", "page=0", "page=-1", "page=no", "page=99999999999999999999999999"} {
		q, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseFilters(q); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
}

func TestTypeAndStatusOrder(t *testing.T) {
	items := []*knowledge.Item{
		{ID: "requirement-aaaaaa", Type: knowledge.TypeRequirement, Status: knowledge.StatusValidated},
		{ID: "persona-ffffff", Type: knowledge.TypePersona, Status: knowledge.StatusRejected},
		{ID: "persona-eeeeee", Type: knowledge.TypePersona, Status: knowledge.StatusProposed},
		{ID: "persona-dddddd", Type: knowledge.TypePersona, Status: knowledge.StatusContested},
		{ID: "persona-cccccc", Type: knowledge.TypePersona, Status: knowledge.StatusNeedsReview},
		{ID: "persona-bbbbbb", Type: knowledge.TypePersona, Status: knowledge.StatusValidated},
		{ID: "persona-aaaaaa", Type: knowledge.TypePersona, Status: knowledge.StatusValidated},
	}
	m := listModel(items, filters{Page: 1, Status: "all"})
	var ids []string
	for _, it := range m.Items {
		ids = append(ids, it.ID)
	}
	want := []string{"persona-aaaaaa", "persona-bbbbbb", "persona-cccccc", "persona-dddddd", "persona-eeeeee", "persona-ffffff", "requirement-aaaaaa"}
	if !slices.Equal(ids, want) {
		t.Fatalf("IDs = %v, want %v", ids, want)
	}
}

func TestNextPending(t *testing.T) {
	status := map[string]knowledge.Status{
		"persona-aaaaaa": knowledge.StatusValidated,
		"persona-bbbbbb": knowledge.StatusProposed,
		"persona-cccccc": knowledge.StatusRejected,
		"persona-dddddd": knowledge.StatusContested,
	}
	var items []*knowledge.Item
	for id, s := range status {
		items = append(items, &knowledge.Item{ID: id, Type: knowledge.TypePersona, Status: s})
	}
	all := filters{Status: statusAll, Page: 1}
	for _, tt := range []struct {
		f             filters
		current, want string
	}{
		{all, "persona-bbbbbb", "persona-dddddd"},
		// The search wraps around to the start of the list.
		{all, "persona-dddddd", "persona-bbbbbb"},
		{all, "persona-aaaaaa", "persona-dddddd"},
		{filters{Status: statusPending, Page: 1}, "persona-bbbbbb", "persona-dddddd"},
		// An item outside the list leads to the first pending item.
		{filters{Status: statusPending, Page: 1}, "persona-cccccc", "persona-dddddd"},
		// The only pending item stays.
		{filters{Status: "proposed", Page: 1}, "persona-bbbbbb", "persona-bbbbbb"},
	} {
		if got, page := nextPending(items, tt.f, tt.current); got != tt.want || page != 1 {
			t.Errorf("next after %s in %+v = %s (page %d), want %s", tt.current, tt.f, got, page, tt.want)
		}
	}
	var many []*knowledge.Item
	for i := range 60 {
		many = append(many, &knowledge.Item{ID: fmt.Sprintf("persona-%06d", i), Type: knowledge.TypePersona, Status: knowledge.StatusProposed})
	}
	if id, page := nextPending(many, filters{Status: statusPending, Page: 1}, "persona-000049"); id != "persona-000050" || page != 2 {
		t.Fatalf("next = %s page %d", id, page)
	}
}

func TestParseNext(t *testing.T) {
	for _, tt := range []struct {
		raw, id string
		f       filters
	}{
		{"/knowledge/persona-k3x9q2", "persona-k3x9q2", filters{Status: statusPending, Page: 1}},
		{"/knowledge/requirement-8fz2mc?q=a+%26+b&type=requirement&status=all&page=2", "requirement-8fz2mc", filters{Query: "a & b", Type: "requirement", Status: statusAll, Page: 2}},
	} {
		id, f, err := parseNext(tt.raw)
		if err != nil || id != tt.id || f != tt.f {
			t.Errorf("parseNext(%q) = %q %+v %v", tt.raw, id, f, err)
		}
	}
	for _, raw := range []string{
		"https://evil.example/knowledge/persona-k3x9q2",
		"//evil.example/knowledge/persona-k3x9q2",
		"/\\evil.example/knowledge/persona-k3x9q2",
		"/knowledge",
		"/knowledge/",
		"/knowledge/persona-k3x9q2/accept",
		"/knowledge/../assets/app.js",
		"/knowledge/unknown-k3x9q2",
		"/knowledge/persona-k3x9q2#evidence-e1",
		"/knowledge/persona-k3x9q2?actor=human",
		"/knowledge/persona-k3x9q2?q=a&q=b",
		"/knowledge/persona-k3x9q2?status=decided",
		"/knowledge/persona-k3x9q2?page=0",
		"javascript:alert(1)",
		"knowledge/persona-k3x9q2",
	} {
		if _, _, err := parseNext(raw); err == nil {
			t.Errorf("accepted next %q", raw)
		}
	}
}

func TestPresentation(t *testing.T) {
	for _, tt := range []struct{ got, want string }{
		{initials("Léa Martin"), "LM"},
		{initials("  ada  "), "a"},
		{initials("Jean Paul Sartre"), "JP"},
		{initials("Émile zola"), "Éz"},
		{evidenceRange(knowledge.Evidence{StartLine: 40, EndLine: 42}), "40–42"},
		{evidenceRange(knowledge.Evidence{StartLine: 8, EndLine: 8}), "8"},
		{shortCommit("3f1c2a9b8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b"), "3f1c2a9"},
		{blockedLabel([]string{"p1"}), "Resolve p1 to validate"},
		{blockedLabel([]string{"p1", "p3"}), "Resolve p1, p3 to validate"},
	} {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
	it := fixture(t, "requirement")
	if got := resolveCommand(it, it.OpenPoints[0]); got != `raun resolve requirement-8fz2mc p1 -note "..." [-position N]` {
		t.Errorf("resolve = %q", got)
	}
	if got := resolveCommand(it, knowledge.OpenPoint{ID: "p2", Kind: knowledge.PointQuestion}); got != `raun resolve requirement-8fz2mc p2 -note "..."` {
		t.Errorf("resolve = %q", got)
	}
}
