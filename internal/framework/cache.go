// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// ResolveArtifact reuses official immutable release/digest validation for node
// transport. It does not replace the caller's full compatibility selection.
func (r Resolver) ResolveArtifact(ctx context.Context, source, version string) (Release, error) {
	r = r.defaults()
	switch source {
	case "s2script":
		if !exactVersion.MatchString(version) {
			return Release{}, errors.New("exact S2Script version required")
		}
		v := normalized(version)
		return r.release(ctx, "s2script/s2script", "v"+v, "prerelease", func(n string) bool { return n == "s2script-cs2-linux-"+v+".zip" })
	case "metamod":
		m := regexp.MustCompile(`^2\.0\.0-git([0-9]+)$`).FindStringSubmatch(version)
		if len(m) != 2 {
			return Release{}, errors.New("exact compatible Metamod version required")
		}
		v, e := r.release(ctx, "alliedmodders/metamod-source", "2.0.0."+m[1], "prerelease", func(n string) bool { return n == "mmsource-"+version+"-linux.tar.gz" })
		v.Version = version
		return v, e
	case "mam":
		if strings.TrimPrefix(version, "v") != "1.6.2" {
			return Release{}, errors.New("unsupported MAM version")
		}
		return r.release(ctx, "Source2ZE/MultiAddonManager", "v1.6.2", "stable", func(n string) bool { return n == "MultiAddonManager-v1.6.2-steamrt3.tar.gz" })
	}
	return Release{}, errors.New("unapproved framework source")
}
