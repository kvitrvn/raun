package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/kvitrvn/raun/internal/knowledge"
)

// ReportVersion is the analyst report contract version.
const ReportVersion = 1

// Report is what an analyst agent returns. See docs/agents.md.
type Report struct {
	Version         int              `json:"version"`
	Observations    []Observation    `json:"observations"`
	Interpretations []Interpretation `json:"interpretations"`
	Questions       []Question       `json:"questions"`
}

// Observation is a factual statement with at least one citation.
type Observation struct {
	ID        string     `json:"id"`
	Statement string     `json:"statement"`
	Evidence  []Citation `json:"evidence"`
}

// Citation points to whole lines of a repository file.
type Citation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Excerpt   string `json:"excerpt"`
}

// Interpretation is a candidate knowledge item.
type Interpretation struct {
	ID        string           `json:"id"`
	Type      knowledge.Type   `json:"type"`
	Title     string           `json:"title"`
	Statement string           `json:"statement"`
	Nature    knowledge.Nature `json:"nature"`
	// Observations holds IDs of observations of the same report.
	Observations []string            `json:"observations"`
	Persona      *PersonaContent     `json:"persona,omitempty"`
	Requirement  *RequirementContent `json:"requirement,omitempty"`
}

// PersonaContent mirrors knowledge.Persona.
type PersonaContent struct {
	Description  string   `json:"description"`
	Goals        []string `json:"goals"`
	Capabilities []string `json:"capabilities"`
}

// RequirementContent mirrors knowledge.Requirement, except that Personas
// holds IDs of persona interpretations of the same report.
type RequirementContent struct {
	Statement string                    `json:"statement"`
	Kind      knowledge.RequirementKind `json:"kind"`
	Rationale string                    `json:"rationale"`
	Personas  []string                  `json:"personas"`
}

// Question is something the agent could not settle and a human should.
type Question struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	// About holds IDs of interpretations the question concerns; it may be
	// empty for questions about the project as a whole.
	About []string `json:"about"`
}

// ErrNoJSON reports an output that contains no JSON object.
var ErrNoJSON = errors.New("no JSON object found in agent output")

// ExtractJSON returns the last top-level JSON object in out. Agents may
// print logs or wrap the object in a Markdown fence around it.
func ExtractJSON(out []byte) ([]byte, error) {
	var last []byte
	for i := 0; i < len(out); {
		j := bytes.IndexByte(out[i:], '{')
		if j < 0 {
			break
		}
		start := i + j
		dec := json.NewDecoder(bytes.NewReader(out[start:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			i = start + 1
			continue
		}
		end := start + int(dec.InputOffset())
		last = out[start:end]
		i = end
	}
	if last == nil {
		return nil, ErrNoJSON
	}
	return last, nil
}

// ParseReport extracts, strictly decodes and validates an analyst report.
// Interpretations must use one of the allowed knowledge types.
func ParseReport(out []byte, allowed []knowledge.Type) (*Report, []byte, error) {
	data, err := ExtractJSON(out)
	if err != nil {
		return nil, nil, err
	}
	var r Report
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, data, fmt.Errorf("decode report: %w", err)
	}
	if err := r.Validate(allowed); err != nil {
		return nil, data, err
	}
	return &r, data, nil
}

var reportIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// FieldError reports an invalid value at a field path inside a report.
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Msg }

// Validate checks the report's consistency. All problems are reported at
// once, joined, each as a *FieldError.
func (r *Report) Validate(allowed []knowledge.Type) error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}
	nonEmpty := func(field, s string) {
		if strings.TrimSpace(s) == "" {
			add(field, "must not be empty")
		}
	}
	ids := map[string]string{} // id -> kind, shared namespace
	id := func(field, value, kind string) {
		switch {
		case !reportIDPattern.MatchString(value):
			add(field, "must be a short identifier such as \"o1\", got %q", value)
		case ids[value] != "":
			add(field, "duplicate id %q", value)
		default:
			ids[value] = kind
		}
	}

	if r.Version != ReportVersion {
		add("version", "must be %d, got %d", ReportVersion, r.Version)
	}

	for i, o := range r.Observations {
		f := fmt.Sprintf("observations[%d]", i)
		id(f+".id", o.ID, "observation")
		nonEmpty(f+".statement", o.Statement)
		if len(o.Evidence) == 0 {
			add(f+".evidence", "an observation needs at least one citation")
		}
		for j, c := range o.Evidence {
			cf := fmt.Sprintf("%s.evidence[%d]", f, j)
			if clean := path.Clean(c.Path); c.Path == "" || path.IsAbs(c.Path) || clean != c.Path || clean == ".." || strings.HasPrefix(clean, "../") {
				add(cf+".path", "must be a clean path relative to the repository root, got %q", c.Path)
			}
			if c.StartLine < 1 || c.EndLine < c.StartLine {
				add(cf+".start_line", "invalid line range %d-%d", c.StartLine, c.EndLine)
			}
			nonEmpty(cf+".excerpt", c.Excerpt)
		}
	}

	for i, it := range r.Interpretations {
		id(fmt.Sprintf("interpretations[%d].id", i), it.ID, "interpretation:"+string(it.Type))
	}
	for i, it := range r.Interpretations {
		f := fmt.Sprintf("interpretations[%d]", i)
		if !slices.Contains(allowed, it.Type) {
			add(f+".type", "must be one of %v, got %q", allowed, it.Type)
		}
		nonEmpty(f+".title", it.Title)
		nonEmpty(f+".statement", it.Statement)
		switch it.Nature {
		case knowledge.NatureSupported:
			if len(it.Observations) == 0 {
				add(f+".observations", "a supported interpretation needs at least one observation")
			}
		case knowledge.NatureHypothesis:
		default:
			add(f+".nature", "must be %q or %q, got %q", knowledge.NatureSupported, knowledge.NatureHypothesis, it.Nature)
		}
		for j, ref := range it.Observations {
			if ids[ref] != "observation" {
				add(fmt.Sprintf("%s.observations[%d]", f, j), "unknown observation %q", ref)
			}
		}

		if (it.Persona != nil) != (it.Type == knowledge.TypePersona) {
			add(f+".persona", "must be set if and only if type is %q", knowledge.TypePersona)
		}
		if (it.Requirement != nil) != (it.Type == knowledge.TypeRequirement) {
			add(f+".requirement", "must be set if and only if type is %q", knowledge.TypeRequirement)
		}
		if p := it.Persona; p != nil {
			nonEmpty(f+".persona.description", p.Description)
			for j, g := range p.Goals {
				nonEmpty(fmt.Sprintf("%s.persona.goals[%d]", f, j), g)
			}
			for j, c := range p.Capabilities {
				nonEmpty(fmt.Sprintf("%s.persona.capabilities[%d]", f, j), c)
			}
		}
		if q := it.Requirement; q != nil {
			nonEmpty(f+".requirement.statement", q.Statement)
			if !slices.Contains([]knowledge.RequirementKind{knowledge.KindBusinessRule, knowledge.KindFunctional, knowledge.KindConstraint}, q.Kind) {
				add(f+".requirement.kind", "must be %q, %q or %q, got %q", knowledge.KindBusinessRule, knowledge.KindFunctional, knowledge.KindConstraint, q.Kind)
			}
			for j, ref := range q.Personas {
				if ids[ref] != "interpretation:"+string(knowledge.TypePersona) {
					add(fmt.Sprintf("%s.requirement.personas[%d]", f, j), "unknown persona interpretation %q", ref)
				}
			}
		}
	}

	for i, q := range r.Questions {
		f := fmt.Sprintf("questions[%d]", i)
		id(f+".id", q.ID, "question")
		nonEmpty(f+".question", q.Question)
		for j, ref := range q.About {
			if !strings.HasPrefix(ids[ref], "interpretation:") {
				add(fmt.Sprintf("%s.about[%d]", f, j), "unknown interpretation %q", ref)
			}
		}
	}

	return errors.Join(errs...)
}
