package knowledge

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestEncodeGolden(t *testing.T) {
	tests := []struct {
		golden string
		item   *Item
	}{
		{"persona.yaml", samplePersona()},
		{"requirement.yaml", sampleRequirement()},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			got, err := Encode(tt.item)
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join("testdata", tt.golden)
			if *update {
				if err := os.WriteFile(p, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("Encode() differs from %s (run with -update to accept):\n%s", p, got)
			}

			// Round-trip: decoding then re-encoding yields identical bytes.
			it, err := Decode(want)
			if err != nil {
				t.Fatalf("Decode(golden) error = %v", err)
			}
			again, err := Encode(it)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, want) {
				t.Errorf("round-trip changed the bytes:\n%s", again)
			}
		})
	}
}

func TestDecodeRejects(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "persona.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{"unknown field", strings.Replace(string(golden), "title:", "confidence: 0.9\ntitle:", 1), "field confidence not found"},
		{"invalid content", strings.Replace(string(golden), "status: proposed", "status: validated", 1), "status: must equal the last history event"},
		{"not yaml", "{", "decode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Decode() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestEncodeIndentedExcerpts(t *testing.T) {
	for _, excerpt := range []string{
		"\tif ok {\n\t\treturn\n\t}",
		"    indented\nnot indented",
		"plain\n\tthen tab",
		"trailing space \nline",
		"single line with \t tab",
	} {
		it := samplePersona()
		it.Evidence[0].Excerpt = excerpt
		it.Evidence[0].SHA256 = HashExcerpt(excerpt)
		data, err := Encode(it)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(data)
		if err != nil {
			t.Fatalf("excerpt %q: Decode(Encode()) error = %v\n%s", excerpt, err, data)
		}
		if got.Evidence[0].Excerpt != excerpt {
			t.Errorf("excerpt %q came back as %q", excerpt, got.Evidence[0].Excerpt)
		}
	}
}

func TestEvidenceYAMLMirrorsEvidence(t *testing.T) {
	tags := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			out = append(out, rt.Field(i).Tag.Get("yaml"))
		}
		return out
	}
	if a, b := tags(Evidence{}), tags(evidenceYAML{}); !slices.Equal(a, b) {
		t.Errorf("evidenceYAML fields %v differ from Evidence fields %v", b, a)
	}
}
