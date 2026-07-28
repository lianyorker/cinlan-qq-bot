package security

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var ErrOutsideAllowedRoot = errors.New("path is outside the allowed root")

type Boundary struct {
	root string
}

func NewBoundary(root string) (*Boundary, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("allowed root is empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve allowed root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve allowed root target: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat allowed root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("allowed root is not a directory")
	}
	return &Boundary{root: filepath.Clean(resolved)}, nil
}

func (b *Boundary) Root() string {
	if b == nil {
		return ""
	}
	return b.root
}

func (b *Boundary) Resolve(path string) (string, error) {
	if b == nil || b.root == "" {
		return "", errors.New("allowed root is not configured")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.root, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	resolved, err := resolveExistingPrefix(absolute)
	if err != nil {
		return "", err
	}
	if !b.contains(resolved) {
		return "", ErrOutsideAllowedRoot
	}
	return filepath.Clean(resolved), nil
}

func (b *Boundary) Contains(path string) bool {
	_, err := b.Resolve(path)
	return err == nil
}

func (b *Boundary) contains(path string) bool {
	relative, err := filepath.Rel(b.root, filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func resolveExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve path target: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve path target: %w", err)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}
