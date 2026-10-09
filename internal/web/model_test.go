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
	items := []*knowledge.Item{requirement, rejected, persona}
	for _, tt := range []struct {
		query string
		ids   []string
	}{
		{"", []string{persona.ID, requirement.ID}},
		{"status=all", []string{persona.ID, rejected.ID, requirement.ID}},
		{"status=rejected", []string{rejected.ID}},
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
			count := 0
			for _, c := range m.Counts {
				count += c.Count
			}
			if count != 3 {
				t.Errorf("workspace count = %d", count)
			}
		})
	}
	if items[0] != requirement {
		t.Fatal("list changed input order")
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
	u, err := url.Parse(f.pageURL(3))
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseFilters(u.Query())
	if err != nil {
		t.Fatal(err)
	}
	f.Page = 3
	if got != f {
		t.Fatalf("lost filters: %+v", got)
	}
}

func TestInvalidFilters(t *testing.T) {
	for _, query := range []string{"type=unknown", "status=unknown", "page=0", "page=-1", "page=no", "page=99999999999999999999999999"} {
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
