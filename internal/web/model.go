package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/report"
)

const pageSize = 50

// Status filter values besides the knowledge statuses.
const (
	statusPending = "pending" // proposed, contested and needs-review; the default view
	statusAll     = "all"
)

type filters struct {
	Query, Type, Status string
	Page                int
}

type statusCount struct {
	Status knowledge.Status
	Count  int
	// Grow is the relative width of the status in the progress bar.
	Grow int
}

// counts are workspace-wide totals, independent of filters.
type counts struct {
	Total, Personas, Requirements int
	Pending, Decided, Blocked     int
	Statuses                      []statusCount
}

func (c counts) status(s knowledge.Status) int {
	for _, sc := range c.Statuses {
		if sc.Status == s {
			return sc.Count
		}
	}
	return 0
}

type pageModel struct {
	Project, Title, Error string
	// Host is the local server address; Author the default decision author.
	Host, Author string
	ReadOnly     bool
	Filters      filters
	// Counts is nil when the knowledge base cannot be read.
	Counts *counts
	// Items is the current page of the filtered list; Total its full length.
	Items        []*knowledge.Item
	Total, Pages int
	Empty        bool
	// Item is the item shown in the detail column. Explicit reports that the
	// URL names it, rather than defaulting to the first listed item.
	Item     *knowledge.Item
	Explicit bool
	Links    []itemLink
	// NotFound is the message of a missing item, shown in the detail column.
	NotFound string
	Palette  []*knowledge.Item
	Decision *decisionModel
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
	if f.Status == "" {
		f.Status = statusPending
	}
	if f.Status != statusPending && f.Status != statusAll && !slices.Contains(knowledge.Statuses, knowledge.Status(f.Status)) {
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

func (f filters) match(it *knowledge.Item) bool {
	if f.Type != "" && string(it.Type) != f.Type {
		return false
	}
	switch f.Status {
	case statusPending, "":
		if !canDecide(it.Status) {
			return false
		}
	case statusAll:
	default:
		if string(it.Status) != f.Status {
			return false
		}
	}
	return strings.Contains(strings.ToLower(searchText(it)), strings.ToLower(f.Query))
}

// filtered returns every item matching f, in list order.
func filtered(items []*knowledge.Item, f filters) []*knowledge.Item {
	var out []*knowledge.Item
	for _, it := range items {
		if f.match(it) {
			out = append(out, it)
		}
	}
	slices.SortFunc(out, compareItems)
	return out
}

func countItems(items []*knowledge.Item) *counts {
	c := &counts{Total: len(items)}
	for _, it := range items {
		switch it.Type {
		case knowledge.TypePersona:
			c.Personas++
		case knowledge.TypeRequirement:
			c.Requirements++
		}
		if canDecide(it.Status) {
			c.Pending++
		} else {
			c.Decided++
		}
		if it.HasUnresolvedDisagreement() {
			c.Blocked++
		}
	}
	for _, status := range report.Order {
		n := 0
		for _, it := range items {
			if it.Status == status {
				n++
			}
		}
		c.Statuses = append(c.Statuses, statusCount{Status: status, Count: n, Grow: growFactor(n, len(items))})
	}
	return c
}

// growFactor maps a count to one of the generated grow-1…grow-100 classes:
// counts are exact ratios up to 100 items, rounded percentages beyond.
func growFactor(n, total int) int {
	if n == 0 {
		return 0
	}
	if total <= 100 {
		return n
	}
	return max(1, (n*100+total/2)/total)
}

func listModel(items []*knowledge.Item, f filters) pageModel {
	m := pageModel{Title: viewTitle(f.Status), Filters: f, Empty: len(items) == 0, Counts: countItems(items)}
	all := filtered(items, f)
	m.Total = len(all)
	m.Pages = max(1, (m.Total+pageSize-1)/pageSize)
	// Clamp before multiplication, including machine-sized page values.
	m.Filters.Page = min(f.Page, m.Pages)
	start := (m.Filters.Page - 1) * pageSize
	m.Items = all[start:min(start+pageSize, m.Total)]
	m.Palette = slices.SortedFunc(slices.Values(items), compareItems)
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
		// A requirement keeps the order in which its personas are declared.
		return links
	}
	for _, other := range items {
		if other.Requirement != nil && slices.Contains(other.Requirement.Personas, it.ID) {
			links = append(links, itemLink{ID: other.ID, Title: other.Title, Status: other.Status})
		}
	}
	slices.SortFunc(links, func(a, b itemLink) int { return cmp.Compare(a.ID, b.ID) })
	return links
}

// nextPending returns the next item awaiting a decision after the current
// one in the filtered list, wrapping around, or the current item itself.
func nextPending(items []*knowledge.Item, f filters, current string) (string, int) {
	all := filtered(items, f)
	i := slices.IndexFunc(all, func(it *knowledge.Item) bool { return it.ID == current })
	order := append(slices.Clone(all[i+1:]), all[:max(i, 0)]...)
	for _, it := range order {
		if it.ID != current && canDecide(it.Status) {
			return it.ID, slices.Index(all, it)/pageSize + 1
		}
	}
	page := f.Page
	if i >= 0 {
		page = i/pageSize + 1
	}
	return current, page
}

func (f filters) query() url.Values {
	q := url.Values{}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.Type != "" {
		q.Set("type", f.Type)
	}
	if f.Status != statusPending && f.Status != "" {
		q.Set("status", f.Status)
	}
	if f.Page > 1 {
		q.Set("page", strconv.Itoa(f.Page))
	}
	return q
}

// url returns the list with these filters, showing item id when set.
func (f filters) url(id string) string {
	path := "/knowledge"
	if id != "" {
		path = itemURL(id)
	}
	if q := f.query().Encode(); q != "" {
		return path + "?" + q
	}
	return path
}

func (f filters) withType(t string) filters {
	f.Type, f.Page = t, 1
	return f
}

func (f filters) withStatus(s string) filters {
	f.Status, f.Page = s, 1
	return f
}

func (f filters) withPage(page int) filters {
	f.Page = page
	return f
}

func (f filters) pageURL(id string, page int) string { return f.withPage(page).url(id) }

var errNext = errors.New("next: expected a knowledge item address of this browser")

// parseNext validates the address followed after a decision: an item path
// and list filters only, so a form cannot redirect elsewhere.
func parseNext(raw string) (string, filters, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Opaque != "" || u.User != nil || u.Host != "" || u.Fragment != "" || strings.HasPrefix(raw, "//") {
		return "", filters{}, errNext
	}
	id, ok := strings.CutPrefix(u.EscapedPath(), "/knowledge/")
	if _, valid := knowledge.TypeOfID(id); !ok || !valid {
		return "", filters{}, errNext
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", filters{}, errNext
	}
	for key, values := range q {
		if !slices.Contains([]string{"q", "type", "status", "page"}, key) || len(values) != 1 {
			return "", filters{}, errNext
		}
	}
	f, err := parseFilters(q)
	if err != nil {
		return "", filters{}, fmt.Errorf("next: %w", err)
	}
	return id, f, nil
}

func itemURL(id string) string     { return "/knowledge/" + url.PathEscape(id) }
func evidenceURL(id string) string { return "#evidence-" + id }
