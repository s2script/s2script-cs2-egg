// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"github.com/s2script/s2script-cs2-egg/internal/releasepack"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: releasectl pack|verify|alias")
	}
	f := flag.NewFlagSet("release", flag.ContinueOnError)
	root := f.String("root", ".", "clean source checkout")
	tag := f.String("tag", "", "exact canonical tag")
	out := f.String("output", "", "release directory")
	goBin := f.String("go", "go", "pinned Go executable")
	assets := f.String("assets", "", "publisher assets JSON")
	candidate := f.String("candidate", "", "image identity JSON")
	current := f.String("current", "", "current image identity JSON; absent means new alias")
	channel := f.String("channel", "", "stable/prerelease")
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	switch os.Args[1] {
	case "pack":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		m, e := releasepack.Build(ctx, releasepack.Options{Root: *root, Tag: *tag, Output: *out, Go: *goBin})
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(m)
	case "verify":
		b, e := os.ReadFile(filepath.Join(*out, releasecontract.ManifestName))
		if e != nil {
			return e
		}
		m, e := releasecontract.Decode(b)
		if e != nil {
			return e
		}
		if *tag != "" && m.Tag != *tag {
			return fmt.Errorf("tag mismatch")
		}
		files := map[string]releasecontract.FileDigest{}
		for _, d := range m.Descriptors() {
			f := releasecontract.FileDigest{SHA256: d.SHA256, Size: d.Size}
			if e = releasepack.VerifyFile(filepath.Join(*out, d.Name), f); e != nil {
				return e
			}
			files[d.Name] = f
		}
		for _, name := range []string{releasecontract.ManifestName, "SHA256SUMS"} {
			d, e := releasepack.Digest(filepath.Join(*out, name))
			if e != nil {
				return e
			}
			files[name] = d
		}
		sums, e := os.ReadFile(filepath.Join(*out, "SHA256SUMS"))
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
			sha, name, ok := strings.Cut(line, "  ")
			d, exists := files[name]
			if !ok || !exists || name == "SHA256SUMS" || seen[name] || sha != d.SHA256 {
				return fmt.Errorf("invalid checksum inventory")
			}
			seen[name] = true
		}
		if len(seen) != len(files)-1 {
			return fmt.Errorf("incomplete checksum inventory")
		}
		if *assets != "" {
			b, e := os.ReadFile(*assets)
			if e != nil {
				return e
			}
			var a []releasepack.PublisherAsset
			if e = json.Unmarshal(b, &a); e != nil {
				return e
			}
			if e = releasepack.VerifyPublished(files, a); e != nil {
				return e
			}
		}
		return json.NewEncoder(os.Stdout).Encode(m)
	case "alias":
		var c releasepack.ImageIdentity
		if e := json.Unmarshal([]byte(*candidate), &c); e != nil {
			return e
		}
		var old *releasepack.ImageIdentity
		if *current != "" {
			old = &releasepack.ImageIdentity{}
			if e := json.Unmarshal([]byte(*current), old); e != nil {
				return e
			}
		}
		move, e := releasepack.Promote(*channel, old, c)
		if e != nil {
			return e
		}
		fmt.Println(move)
		return nil
	default:
		return fmt.Errorf("unknown release command")
	}
}
