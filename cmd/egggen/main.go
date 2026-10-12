// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/s2script/s2script-cs2-egg/internal/panel"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, out, errs io.Writer) int {
	flags := flag.NewFlagSet("egggen", flag.ContinueOnError)
	flags.SetOutput(errs)
	image := flags.String("image", "s2script-cs2-egg:dev", "locally built container image reference")
	output := flags.String("output", "pterodactyl/s2script-cs2-egg.json", "egg JSON path")
	catalog := flags.String("catalog-output", "", "optional generated plugin documentation path")
	check := flags.Bool("check", false, "verify generated files without writing")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errs, "unexpected arguments")
		return 2
	}
	data, err := panel.Generate(*image)
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	files := []struct {
		path string
		data []byte
	}{{*output, data}}
	if *catalog != "" {
		files = append(files, struct {
			path string
			data []byte
		}{*catalog, panel.CatalogMarkdown()})
	}
	for _, file := range files {
		if *check {
			existing, err := os.ReadFile(file.path)
			if err != nil || !bytes.Equal(existing, file.data) {
				fmt.Fprintf(errs, "%s does not match generated output; run egggen to regenerate\n", file.path)
				return 1
			}
			fmt.Fprintln(out, "matches:", file.path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(file.path), 0755); err != nil {
			fmt.Fprintln(errs, err)
			return 1
		}
		if err := os.WriteFile(file.path, file.data, 0644); err != nil {
			fmt.Fprintln(errs, err)
			return 1
		}
		fmt.Fprintln(out, "generated:", file.path)
	}
	return 0
}
