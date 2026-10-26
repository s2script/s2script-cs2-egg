// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"io"
	"strings"
)

// InstallerBanner prints the S2Script and EdgeGamers marks in 72 columns.
// Half blocks preserve their proportions on a typical terminal without needing
// image-protocol support, external assets, or a helper program on the node.
// Color follows the same NO_COLOR/terminal policy as the rest of the wizard.
func InstallerBanner(w io.Writer) {
	var out strings.Builder
	out.WriteByte('\n')
	for y := 0; y < len(installerPixels); y += 2 {
		out.WriteString("  ")
		style := ""
		for x := range len(installerPixels[y]) {
			pair := [2]byte{installerPixels[y][x], installerPixels[y+1][x]}
			cell := installerCells[pair]
			if cell.style != style {
				if style != "" {
					out.WriteString(c("0"))
				}
				if cell.style != "" {
					out.WriteString(c(cell.style))
				}
				style = cell.style
			}
			out.WriteString(cell.glyph)
		}
		if style != "" {
			out.WriteString(c("0"))
		}
		out.WriteByte('\n')
	}
	// Match the 32-column marks and six-column gap above.
	out.WriteString("\n           " + Bold("Source2Script") + "                           " + Bold("EdgeGamers") + "\n")
	fmt.Fprintln(w, out.String())
}

// W = foreground, G = S2Script lime, space = terminal background. Each pair of
// rows becomes one terminal line. Rasterized from the supplied Source2Script
// PNG (under pterodactyl/) and assets/branding/edgegamers-mark.webp, without margins.
var installerPixels = [...]string{
	"                                                                   WW ",
	"        WWWWWWWW WWWWWWW                                       WWWWW  ",
	"       WWWWWWWWW WWWWWWW                       WWWWWWWWWWWWWWWWWWW    ",
	"       WWWWWWWW  WWWWWWWW                     WWWWWWWWWWWWWWWWWW      ",
	"      WWW             WWW                     WWWWWWWWWWWWWWWW        ",
	"      WWW              WWW                    WWWWWWWWWWWWWW          ",
	"      WWW             WWW                    WWWWWW                   ",
	"       WWW        GG WWWW  G                 WWWWWW            WW     ",
	"  G    WWWWWWWW  GWGWWWW  GG G               WWWWW           WWW      ",
	" G      WWWWWWW  GWGWWWW  G   G              WWWWW          WWWW      ",
	"GG          WWW  GGG     GG    G             WWWWW        WWWWW       ",
	" G          WWW GGG     GG    G             WWWW          WWWWW       ",
	"  G        WWW  GGG     GG   G              WWW           WWWWW       ",
	"           GGW GGG      G                  WW            WWWWWW       ",
	"          GGG GGGG                                       WWWWWW       ",
	"      GGGGGG  GGG                               WWWWWWWWWWWWWW        ",
	"     GGGGGGG GGGG                             WWWWWWWWWWWWWWWW        ",
	"    GWGG     GGGGGGGGGGG                    WWWWWWWWWWWWWWWWWW        ",
	"    GWG       GGGGGGGGG                   WWWWWWWWWWWWWWWWWWW         ",
	"    GGG                                 WWWWW                         ",
	"                                       WW                             ",
	"                                                                      ",
}

var installerCells = map[[2]byte]struct{ glyph, style string }{
	{' ', ' '}: {" ", ""},
	{'W', ' '}: {"▀", "1"},
	{' ', 'W'}: {"▄", "1"},
	{'W', 'W'}: {"█", "1"},
	{'G', ' '}: {"▀", "38;2;132;198;0"},
	{' ', 'G'}: {"▄", "38;2;132;198;0"},
	{'G', 'G'}: {"█", "38;2;132;198;0"},
	{'W', 'G'}: {"▀", "97;48;2;132;198;0"},
	{'G', 'W'}: {"▀", "38;2;132;198;0;107"},
}
