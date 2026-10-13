// SPDX-License-Identifier: GPL-3.0-or-later

// Command eggvariants regenerates the standalone (no node daemon)
// Pterodactyl egg export from the canonical, hand-maintained one. Run
// after editing pterodactyl/s2script-cs2-egg.json.
//
//go:generate go run . -root ../..
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/K4ryuu/CS2-Egg-Go/internal/eggvariants"
)

const (
	src = "pterodactyl/s2script-cs2-egg.json"
	out = "pterodactyl/s2script-cs2-egg-standalone.json"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := os.Chdir(filepath.Clean(*root)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "eggvariants:", err)
		os.Exit(1)
	}
}

func run() error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	standalone, err := eggvariants.Standalone(data)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	return os.WriteFile(out, standalone, 0o644)
}
