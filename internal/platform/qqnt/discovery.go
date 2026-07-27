package qqnt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maximumQQPackageBytes = 1024 * 1024

type installation struct {
	QQExecutable string
	Version      string
	AppDir       string
	WrapperPath  string
	PackagePath  string
}

type packageMetadata struct {
	Version      string `json:"version"`
	BuildVersion string `json:"buildVersion"`
}

func inspectInstallation(qqExecutable string) (installation, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(qqExecutable))
	if err != nil {
		return installation{}, fmt.Errorf("resolve QQ executable: %w", err)
	}
	if info, err := os.Stat(absolute); err != nil || info.IsDir() {
		if err == nil {
			err = fmt.Errorf("path is a directory")
		}
		return installation{}, fmt.Errorf("QQ executable %q is invalid: %w", absolute, err)
	}
	versionRoot := filepath.Join(filepath.Dir(absolute), "versions")
	entries, err := os.ReadDir(versionRoot)
	if err != nil {
		return installation{}, fmt.Errorf("read QQ versions directory: %w", err)
	}

	var (
		selected      installation
		selectedBuild int64 = -1
	)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		appDir := filepath.Join(versionRoot, entry.Name(), "resources", "app")
		wrapperPath := filepath.Join(appDir, "wrapper.node")
		packagePath := filepath.Join(appDir, "package.json")
		if !regularFile(wrapperPath) || !regularFile(packagePath) {
			continue
		}
		metadata, loadErr := readPackageMetadata(packagePath)
		if loadErr != nil {
			continue
		}
		build, parseErr := strconv.ParseInt(metadata.BuildVersion, 10, 64)
		if parseErr != nil {
			build = 0
		}
		if selected.AppDir == "" || build > selectedBuild ||
			(build == selectedBuild && entry.Name() > filepath.Base(filepath.Dir(filepath.Dir(selected.AppDir)))) {
			selectedBuild = build
			selected = installation{
				QQExecutable: absolute,
				Version:      metadata.Version,
				AppDir:       appDir,
				WrapperPath:  wrapperPath,
				PackagePath:  packagePath,
			}
		}
	}
	if selected.AppDir == "" {
		return installation{}, fmt.Errorf(
			"no usable QQNT version was found under %s",
			versionRoot,
		)
	}
	return selected, nil
}

func writePatchedPackage(sourcePath, targetPath string) error {
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read official QQ package metadata: %w", err)
	}
	if len(content) > maximumQQPackageBytes {
		return fmt.Errorf("official QQ package metadata exceeds %d bytes", maximumQQPackageBytes)
	}
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode official QQ package metadata: %w", err)
	}
	document["main"] = "./loadCinlan.js"
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Cinlan QQ package metadata: %w", err)
	}
	encoded = append(encoded, '\n')

	absolute, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolve patched QQ package path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return fmt.Errorf("create patched QQ package directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(absolute), ".qqnt-package-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary QQ package metadata: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary QQ package metadata: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary QQ package metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary QQ package metadata: %w", err)
	}
	if err := os.Rename(temporaryPath, absolute); err != nil {
		_ = os.Remove(absolute)
		if retryErr := os.Rename(temporaryPath, absolute); retryErr != nil {
			return fmt.Errorf("replace patched QQ package metadata: %w", err)
		}
	}
	return nil
}

func readPackageMetadata(path string) (packageMetadata, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return packageMetadata{}, err
	}
	if len(content) > maximumQQPackageBytes {
		return packageMetadata{}, fmt.Errorf("package metadata exceeds size limit")
	}
	var metadata packageMetadata
	if err := json.Unmarshal(content, &metadata); err != nil {
		return packageMetadata{}, err
	}
	if strings.TrimSpace(metadata.Version) == "" {
		metadata.Version = filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	}
	return metadata, nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func absoluteFile(path, label string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	if !regularFile(absolute) {
		return "", fmt.Errorf("%s does not exist: %s", label, absolute)
	}
	return absolute, nil
}
