package run

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

// ManifestVersion is the run manifest format version.
const ManifestVersion = 1

// Run statuses.
const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Agent statuses.
const (
	AgentOK     = "ok"
	AgentFailed = "failed"
)

// Manifest describes a run well enough to understand and reproduce it. It
// is versioned with the knowledge base; raw prompts and outputs stay local.
type Manifest struct {
	Version      int           `yaml:"version"`
	ID           string        `yaml:"id"`
	Status       string        `yaml:"status"`
	Error        string        `yaml:"error,omitempty"`
	StartedAt    time.Time     `yaml:"started_at"`
	FinishedAt   time.Time     `yaml:"finished_at"`
	Commit       string        `yaml:"commit"`
	RaunVersion  string        `yaml:"raun_version"`
	ConfigSHA256 string        `yaml:"config_sha256"`
	Language     string        `yaml:"language"`
	Types        []string      `yaml:"types"`
	Quorum       int           `yaml:"quorum"`
	Agents       []AgentRecord `yaml:"agents"`
	// Items lists the knowledge items this run created.
	Items []string `yaml:"items,omitempty"`
	// Questions are agent questions not attached to any item.
	Questions []QuestionRecord `yaml:"questions,omitempty"`
}

// AgentRecord describes one agent's participation in a run.
type AgentRecord struct {
	ID                 string   `yaml:"id"`
	Role               string   `yaml:"role"`
	Instructions       string   `yaml:"instructions"`
	InstructionsSHA256 string   `yaml:"instructions_sha256"`
	Argv               []string `yaml:"argv"`
	PromptSHA256       string   `yaml:"prompt_sha256"`
	Status             string   `yaml:"status"`
	Error              string   `yaml:"error,omitempty"`
	ExitCode           int      `yaml:"exit_code"`
	Duration           string   `yaml:"duration"`
	// SnapshotChanges lists files the agent changed in its copy.
	SnapshotChanges []string       `yaml:"snapshot_changes,omitempty"`
	Report          *ReportSummary `yaml:"report,omitempty"`
}

// ReportSummary counts what a valid report contained and how its evidence
// fared.
type ReportSummary struct {
	Observations    int                `yaml:"observations"`
	Interpretations int                `yaml:"interpretations"`
	Questions       int                `yaml:"questions"`
	Evidence        EvidenceCounts     `yaml:"evidence"`
	Rejected        []RejectedEvidence `yaml:"rejected_evidence,omitempty"`
}

// EvidenceCounts counts citations by verification outcome.
type EvidenceCounts struct {
	Verified  int `yaml:"verified"`
	Relocated int `yaml:"relocated"`
	Invalid   int `yaml:"invalid"`
}

// RejectedEvidence is a citation that did not pass verification.
type RejectedEvidence struct {
	Observation string `yaml:"observation"`
	Path        string `yaml:"path"`
	Lines       string `yaml:"lines"`
	Reason      string `yaml:"reason"`
}

// QuestionRecord is an agent question kept at run level.
type QuestionRecord struct {
	Agent    string `yaml:"agent"`
	ID       string `yaml:"id"`
	Question string `yaml:"question"`
}

func (m *Manifest) write(path string) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
