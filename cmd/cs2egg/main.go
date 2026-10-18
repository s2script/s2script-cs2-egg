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
		fmt.Fprintln(os.Stderr, "Plugins are managed by addons/s2script/plugins.json. Use sm plugins commands in the game console or the s2s server CLI.")
		os.Exit(1)
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
