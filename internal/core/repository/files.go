package repository

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNoFile: no file at that path at that commit, or no such branch.
	ErrNoFile = errors.New("no such file")
	// ErrFileTooLarge: the file exceeds the bound of the read; it is
	// refused whole, never truncated.
	ErrFileTooLarge = errors.New("file larger than the bound")
)

// ConfigurationPath is the configuration of a repository, at the root of
// its default branch.
const ConfigurationPath = ".raun.yaml"

// MaxConfigurationSize bounds the configuration of a repository (#33): a
// file larger is unreadable.
const MaxConfigurationSize = 64 << 10

// Files is the optional capability of a forge that reads the files of a
// repository without cloning it, with the narrowest token that reaches it.
// Its errors wrap ErrUnavailable when the forge cannot answer now.
type Files interface {
	// Head returns the commit at the head of branch in the repository with
	// this stable identifier; ErrNoFile when there is no such branch.
	Head(ctx context.Context, repository, branch string) (string, error)
	// Read returns the file at path, from the root, at commit, limit bytes
	// at most. ErrNoFile when there is no file there: a directory or a
	// submodule is not one, nor a link the forge does not resolve; a link
	// the forge resolves to a file of the same repository at the same
	// commit reads as that file, as untrusted. ErrFileTooLarge past limit.
	Read(ctx context.Context, repository, commit, path string, limit int64) ([]byte, error)
}

// ConfigurationState is what Raun knows of the configuration of a
// repository. Epic 8, which decodes the file, adds invalid.
type ConfigurationState string

const (
	// ConfigurationConfigured: the file is there; until epic 8 decodes it,
	// any file counts.
	ConfigurationConfigured ConfigurationState = "configured"
	// ConfigurationMissing: the default branch has no file, or no commit.
	ConfigurationMissing ConfigurationState = "not_configured"
	// ConfigurationUnreadable: the forge could not give the file, or it is
	// larger than MaxConfigurationSize.
	ConfigurationUnreadable ConfigurationState = "unreadable"
)

// Why a configuration is unreadable.
const (
	ReasonTooLarge    = "too_large"
	ReasonUnavailable = "unavailable"
)

// Configuration is the last read of the configuration of a repository: at
// the head of its default branch then, Commit, at ReadAt. Content is the
// file, untrusted, kept for the decoder of epic 8, and only for a
// configured repository; Reason says why it is unreadable.
type Configuration struct {
	State   ConfigurationState
	Commit  string
	Content []byte
	Reason  string
	ReadAt  time.Time
}

// ConfigurationStore keeps the last configuration of each repository Raun
// knows. Every store passes the contract suite of package repositorytest.
type ConfigurationStore interface {
	// PutConfiguration replaces the configuration of the repository of
	// forge with this identifier; ErrNotFound when Raun does not know it.
	PutConfiguration(ctx context.Context, forge, repository string, c Configuration) error
	// Configuration returns it, and false when none was read yet.
	Configuration(ctx context.Context, forge, repository string) (Configuration, bool, error)
}
