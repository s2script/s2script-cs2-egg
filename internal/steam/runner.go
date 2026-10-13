// SPDX-License-Identifier: GPL-3.0-or-later
// Package steam runs SteamCMD with an argument vector, never through a shell.
package steam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/processgroup"
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
func command(ctx context.Context, executable string, args []string, log io.Writer, home string) ([]byte, error) {
	if executable == "" {
		return nil, errors.New("SteamCMD executable is required")
	}
	// Own this setup even for direct Steam callers (including future node work).
	// The setting is process-wide; cleanup below only waits this command's PGID.
	if e := processgroup.EnableReaping(); e != nil {
		return nil, e
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	if home != "" {
		cmd.Dir = filepath.Dir(executable)
		cmd.Env = []string{}
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "HOME=") && !strings.HasPrefix(v, "STEAM_ACC=") && !strings.HasPrefix(v, "DISCORD_WEBHOOK_URL=") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+home)
	}
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
	runErr := cmd.Run() // Wait the direct child before waiting any adopted descendant.
	if cmd.Process != nil {
		runErr = errors.Join(runErr, processgroup.KillAndReap(cmd.Process.Pid))
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return nil, fmt.Errorf("SteamCMD failed (exit code %d)", exitErr.ExitCode())
		}
		return nil, errors.New("SteamCMD could not start or finish; check its executable and console output")
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
	if _, err = command(ctx, executable, args, log, abs); err != nil {
		return "", err
	}
	return InstalledBuild(abs)
}

// Bootstrap initializes an image-provided SteamCMD in writable persistent state.
func Bootstrap(ctx context.Context, executable, root string, log io.Writer) error {
	_, err := command(ctx, executable, []string{"+quit"}, log, root)
	return err
}

// InstalledBuild reads app 730 evidence without invoking SteamCMD or networking.
func InstalledBuild(root string) (string, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return "", errors.New("installed app root is unavailable")
	}
	defer dir.Close()
	if f, e := dir.OpenFile(".s2shared.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0); e == nil {
		defer f.Close()
		var state struct {
			Generation struct {
				BuildID string `json:"build_id"`
			} `json:"generation"`
		}
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || json.NewDecoder(io.LimitReader(f, 64<<20)).Decode(&state) != nil || !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(state.Generation.BuildID) {
			return "", errors.New("invalid shared installed build")
		}
		return state.Generation.BuildID, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	f, err := dir.OpenFile("steamapps/appmanifest_730.acf", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("installed app 730 manifest is missing")
	}
	defer f.Close()
	if info, e := f.Stat(); e != nil || !info.Mode().IsRegular() {
		return "", errors.New("installed app manifest must be regular")
	}
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
	b, err := command(ctx, executable, []string{"+login", "anonymous", "+app_info_update", "1", "+app_info_print", "730", "+quit"}, nil, "")
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
