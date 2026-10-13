// SPDX-License-Identifier: GPL-3.0-or-later

// cs2egg is the image entrypoint of the KitsuneLab CS2 egg.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/boot"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"path/filepath"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(boot.Describe())
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	root := os.Getenv("HOME")
	if root == "" {
		root = "/home/container"
	}
	if len(os.Args) > 1 && os.Args[1] == "plugins" {
		env := addons.Env{Root: root, Temp: filepath.Join(root, "temps"), Versions: addons.Versions{Path: filepath.Join(root, "egg/versions.txt")}, Log: logx.New("S2Script", logx.Info, os.Stdout), HTTP: &http.Client{Timeout: 90 * time.Second}}
		if err := env.S2ScriptPluginCommand(ctx, os.Args[2:], os.Getenv, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	unlock, err := addons.AcquireS2ScriptRuntime(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer unlock()
	code := boot.Run(ctx, boot.Env{
		Root:   root,
		Getenv: os.Getenv,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		HTTP:   &http.Client{Timeout: 90 * time.Second},
	})
	// os.Exit skips defers; release the cooperative runtime guard explicitly.
	unlock()
	os.Exit(code)
}
