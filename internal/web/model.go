package web

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/report"
)

const pageSize = 50

type filters struct {
	Query, Type, Status string
	Page                int
}

type statusCount struct {
	Status knowledge.Status
	Count  int
}

type pageModel struct {
	Project, Title, Error string
	Filters               filters
	Counts                []statusCount
	Items                 []*knowledge.Item
	Item                  *knowledge.Item
	Links                 []itemLink
	Total, Pages          int
	Empty                 bool
	ReadOnly              bool
	Decision              *decisionModel
}

type itemLink struct {
	ID, Title string
	Status    knowledge.Status
	Missing   bool
}

func parseFilters(q url.Values) (filters, error) {
	f := filters{Query: strings.TrimSpace(q.Get("q")), Type: q.Get("type"), Status: q.Get("status"), Page: 1}
	if f.Type != "" && !slices.Contains(knowledge.Types, knowledge.Type(f.Type)) {
		return f, fmt.Errorf("type: unknown type %q", f.Type)
	}
	if f.Status != "" && f.Status != "all" && !slices.Contains(knowledge.Statuses, knowledge.Status(f.Status)) {
		return f, fmt.Errorf("status: unknown status %q", f.Status)
	}
	if p := q.Get("page"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			return f, fmt.Errorf("page: must be a positive integer")
		}
		f.Page = n
	}
	return f, nil
}

func listModel(items []*knowledge.Item, f filters) pageModel {
	m := pageModel{Title: "Knowledge", Filters: f, Empty: len(items) == 0}
	for _, status := range report.Order {
		count := 0
		for _, it := range items {
			if it.Status == status {
				count++
			}
		}
		m.Counts = append(m.Counts, statusCount{status, count})
	}
	query := strings.ToLower(f.Query)
	for _, it := range items {
		if f.Type != "" && string(it.Type) != f.Type {
			continue
		}
		if f.Status == "" && it.Status == knowledge.StatusRejected {
			continue
		}
		if f.Status != "" && f.Status != "all" && string(it.Status) != f.Status {
			continue
		}
		if !strings.Contains(strings.ToLower(searchText(it)), query) {
			continue
		}
		m.Items = append(m.Items, it)
	}
	slices.SortFunc(m.Items, compareItems)
	m.Total = len(m.Items)
	m.Pages = max(1, (m.Total+pageSize-1)/pageSize)
	// Clamp before multiplication, including machine-sized page values.
	m.Filters.Page = min(f.Page, m.Pages)
	start := (m.Filters.Page - 1) * pageSize
	m.Items = m.Items[start:min(start+pageSize, m.Total)]
	return m
}

func compareItems(a, b *knowledge.Item) int {
	if n := cmp.Compare(slices.Index(knowledge.Types, a.Type), slices.Index(knowledge.Types, b.Type)); n != 0 {
		return n
	}
	if n := cmp.Compare(slices.Index(report.Order, a.Status), slices.Index(report.Order, b.Status)); n != 0 {
		return n
	}
	return cmp.Compare(a.ID, b.ID)
}

func searchText(it *knowledge.Item) string {
	parts := []string{it.ID, it.Title}
	if p := it.Persona; p != nil {
		parts = append(parts, p.Description)
		parts = append(parts, p.Goals...)
		parts = append(parts, p.Capabilities...)
	}
	if r := it.Requirement; r != nil {
		parts = append(parts, r.Statement, string(r.Kind), r.Rationale)
		parts = append(parts, r.Personas...)
	}
	return strings.Join(parts, "\n")
}

func businessText(it *knowledge.Item) string {
	if it.Persona != nil {
		return it.Persona.Description
	}
	return it.Requirement.Statement
}

func relatedItems(it *knowledge.Item, items []*knowledge.Item) []itemLink {
	var links []itemLink
	if it.Requirement != nil {
		for _, id := range it.Requirement.Personas {
			link := itemLink{ID: id, Missing: true}
			for _, other := range items {
				if other.ID == id {
					link.Title = other.Title
					link.Status = other.Status
					link.Missing = false
					break
				}
			}
			links = append(links, link)
		}
	} else {
		for _, other := range items {
			if other.Requirement != nil && slices.Contains(other.Requirement.Personas, it.ID) {
				links = append(links, itemLink{ID: other.ID, Title: other.Title, Status: other.Status})
			}
		}
	}
	slices.SortFunc(links, func(a, b itemLink) int { return cmp.Compare(a.ID, b.ID) })
	return links
}

func (f filters) pageURL(page int) string {
	q := url.Values{}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.Type != "" {
		q.Set("type", f.Type)
	}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	q.Set("page", strconv.Itoa(page))
	return "/knowledge?" + q.Encode()
}

func itemURL(id string) string     { return "/knowledge/" + url.PathEscape(id) }
func evidenceURL(id string) string { return "#evidence-" + id }
