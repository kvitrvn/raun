// Package workspace manages the `.raun/` directory inside an analyzed
// repository.
package workspace

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Dir is the name of the Raun directory at the repository root.
const Dir = ".raun"

// ConfigFile is the configuration file name inside Dir.
const ConfigFile = "config.yaml"

//go:embed templates
var templates embed.FS

// scaffold maps files created by Init (relative to Dir) to their template.
var scaffold = []struct{ name, template string }{
	{ConfigFile, "templates/config.yaml"},
	{"context.md", "templates/context.md"},
	{".gitignore", "templates/gitignore"},
}

// ConfigPath returns the configuration file path for the repository at root.
func ConfigPath(root string) string {
	return filepath.Join(root, Dir, ConfigFile)
}

// InitResult lists the files Init created and those it left untouched,
// as paths relative to the repository root.
type InitResult struct {
	Created []string
	Skipped []string
}

// Init creates the Raun directory and its starter files under root.
// Existing files are never overwritten, so Init is safe to run repeatedly.
func Init(root string) (InitResult, error) {
	var res InitResult
	if err := os.MkdirAll(filepath.Join(root, Dir), 0o755); err != nil {
		return res, fmt.Errorf("create %s: %w", Dir, err)
	}

	for _, f := range scaffold {
		rel := filepath.Join(Dir, f.name)
		data, err := templates.ReadFile(f.template)
		if err != nil {
			return res, fmt.Errorf("read template %s: %w", f.template, err)
		}

		created, err := writeNew(filepath.Join(root, rel), data)
		if err != nil {
			return res, fmt.Errorf("write %s: %w", rel, err)
		}
		if created {
			res.Created = append(res.Created, rel)
		} else {
			res.Skipped = append(res.Skipped, rel)
		}
	}
	return res, nil
}

// writeNew writes data to p only if p does not exist yet. It reports whether
// the file was created.
func writeNew(p string, data []byte) (bool, error) {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(data); err != nil {
		return false, errors.Join(err, f.Close())
	}
	return true, f.Close()
}
