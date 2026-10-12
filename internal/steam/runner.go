// SPDX-License-Identifier: GPL-3.0-or-later
// Package steam runs SteamCMD with an argument vector, never through a shell.
package steam

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const maxOutput = 8 << 20

type boundedOutput struct {
	data     []byte
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data)+n > maxOutput {
		b.overflow = true
		p = p[:maxOutput-len(b.data)]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func command(ctx context.Context, executable string, args []string, log io.Writer) ([]byte, error) {
	if executable == "" {
		return nil, errors.New("SteamCMD executable is required")
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		e := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if e == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return e
	}
	var output boundedOutput
	var w io.Writer = &output
	if log != nil {
		w = io.MultiWriter(&output, log)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("SteamCMD failed")
	}
	if output.overflow {
		return nil, errors.New("SteamCMD output exceeded limit")
	}
	return output.data, nil
}

// Update installs/updates app 730 and reads its installed manifest. The caller
// serializes SteamCMD with framework preparation and keeps consumers stopped.
func Update(ctx context.Context, executable, root string, validate bool, log io.Writer) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	args := []string{"+force_install_dir", abs, "+login", "anonymous", "+app_update", "730"}
	if validate {
		args = append(args, "validate")
	}
	args = append(args, "+quit")
	if _, err = command(ctx, executable, args, log); err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(abs, "steamapps/appmanifest_730.acf"))
	if err != nil {
		return "", errors.New("installed app 730 manifest is missing")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return "", errors.New("installed app manifest is unreadable or oversized")
	}
	if !regexp.MustCompile(`"appid"\s*"730"`).Match(b) {
		return "", errors.New("installed manifest is not app 730")
	}
	return buildID(b)
}

// LatestBuild asks SteamCMD for the public branch of app 730.
func LatestBuild(ctx context.Context, executable string) (string, error) {
	b, err := command(ctx, executable, []string{"+login", "anonymous", "+app_info_update", "1", "+app_info_print", "730", "+quit"}, nil)
	if err != nil {
		return "", err
	}
	block := regexp.MustCompile(`(?s)"public"\s*\{([^{}]*)\}`).FindSubmatch(b)
	if len(block) != 2 {
		return "", errors.New("SteamCMD public build metadata is missing")
	}
	return buildID(block[1])
}
func buildID(data []byte) (string, error) {
	m := regexp.MustCompile(`"buildid"\s*"([0-9]+)"`).FindSubmatch(data)
	if len(m) != 2 || strings.TrimLeft(string(m[1]), "0") == "" {
		return "", errors.New("Steam build ID is missing")
	}
	return string(m[1]), nil
}
