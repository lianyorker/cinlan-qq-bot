//go:build windows

package qqnt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var uninstallPathPattern = regexp.MustCompile(`(?i)([A-Z]:\\[^\r\n"]*?Uninstall\.exe)`)

func (a *Adapter) launch(ctx context.Context, address, token string) error {
	qqExecutable, err := discoverQQExecutable(a.cfg.QQExecutable)
	if err != nil {
		return err
	}
	if !a.cfg.AllowRunning {
		running, checkErr := qqProcessRunning(ctx)
		if checkErr != nil {
			return checkErr
		}
		if running {
			return fmt.Errorf(
				"QQ.exe is already running without the Cinlan runtime; close QQ and start cinlan-qq-bot again, or set QQNT_ALLOW_RUNNING=true for an isolated second instance",
			)
		}
	}

	current, err := inspectInstallation(qqExecutable)
	if err != nil {
		return err
	}
	loader, err := absoluteFile(a.cfg.LoaderPath, "Cinlan QQNT loader")
	if err != nil {
		return err
	}
	hook, err := absoluteFile(a.cfg.HookPath, "Cinlan QQNT hook")
	if err != nil {
		return err
	}
	loadPath, err := absoluteFile(a.cfg.LoadPath, "Cinlan QQNT entry loader")
	if err != nil {
		return err
	}
	runtimePath, err := absoluteFile(a.cfg.RuntimePath, "Cinlan QQNT runtime")
	if err != nil {
		return err
	}
	patchPath, err := filepath.Abs(a.cfg.PatchPackagePath)
	if err != nil {
		return fmt.Errorf("resolve QQNT patched package path: %w", err)
	}
	if err := writePatchedPackage(current.PackagePath, patchPath); err != nil {
		return err
	}

	launchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	statusFile, err := os.CreateTemp(filepath.Dir(patchPath), ".qqnt-hook-*.status")
	if err != nil {
		return fmt.Errorf("create QQNT hook status file: %w", err)
	}
	statusPath := statusFile.Name()
	statusFile.Close()
	defer os.Remove(statusPath)
	command := exec.CommandContext(launchCtx, loader, current.QQExecutable, hook)
	command.Env = append(os.Environ(),
		"CINLAN_QQNT_PATCH_PACKAGE="+patchPath,
		"CINLAN_QQNT_LOAD_PATH="+loadPath,
		"CINLAN_QQNT_RUNTIME_PATH="+runtimePath,
		"CINLAN_QQNT_WRAPPER_PATH="+current.WrapperPath,
		"CINLAN_QQNT_VERSION="+current.Version,
		"CINLAN_QQNT_IPC_ADDR="+address,
		"CINLAN_QQNT_IPC_TOKEN="+token,
		"CINLAN_QQNT_MAX_FRAME_BYTES="+strconv.Itoa(a.cfg.MaxFrameBytes),
		"CINLAN_QQNT_HEADLESS="+strconv.FormatBool(a.cfg.Headless),
		"CINLAN_QQNT_ACTION_TIMEOUT_MS="+strconv.FormatInt(a.cfg.ActionTimeout.Milliseconds(), 10),
		"CINLAN_QQNT_HOOK_STATUS_PATH="+statusPath,
		"CINLAN_QQNT_SEND_IMAGE_ROOTS="+strings.Join(a.cfg.ImageSendRoots, ";"),
		"CINLAN_QQNT_SEND_IMAGE_MAX_BYTES="+strconv.FormatInt(a.cfg.ImageMaxBytes, 10),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("launch QQNT runtime: %w: %s", err, detail)
		}
		return fmt.Errorf("launch QQNT runtime: %w", err)
	}
	a.logger.Info(
		"QQNT process launched",
		"version", current.Version,
		"executable", current.QQExecutable,
	)
	return nil
}

func discoverQQExecutable(configured string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return absoluteFile(configured, "QQ executable")
	}
	for _, key := range []string{
		`HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\QQ`,
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\QQ`,
		`HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\QQ`,
	} {
		output, err := exec.Command("reg.exe", "query", key, "/v", "UninstallString").CombinedOutput()
		if err != nil {
			continue
		}
		match := uninstallPathPattern.FindStringSubmatch(string(output))
		if len(match) != 2 {
			continue
		}
		candidate := filepath.Join(filepath.Dir(match[1]), "QQ.exe")
		if regularFile(candidate) {
			return filepath.Abs(candidate)
		}
	}
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Tencent", "QQNT", "QQ.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Tencent", "QQNT", "QQ.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Tencent", "QQNT", "QQ.exe"),
	}
	for _, candidate := range candidates {
		if regularFile(candidate) {
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("QQNT installation was not found; set QQNT_PATH to QQ.exe")
}

func qqProcessRunning(ctx context.Context) (bool, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(
		checkCtx,
		"tasklist.exe",
		"/FI",
		"IMAGENAME eq QQ.exe",
		"/FO",
		"CSV",
		"/NH",
	).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("check running QQ processes: %w", err)
	}
	return strings.Contains(strings.ToLower(string(output)), `"qq.exe"`), nil
}
