#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
"""Explicit release publisher. No repository/package visibility changes."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

REPO = "s2script/s2script-cs2-egg"
IMAGE = "ghcr.io/" + REPO
API = "https://api.github.com/repos/" + REPO
SOURCE = "https://github.com/" + REPO
MANIFEST = "s2script-release-v1.json"
RECEIPT = "release-images-v1.json"


def run(*args):
    return subprocess.check_output(args, text=True).strip()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError("Authenticated API redirects are refused")


def request(url, token="", data=None):
    if not url.startswith(("https://api.github.com/", "https://uploads.github.com/")):
        raise RuntimeError("Unexpected API origin")
    headers = {"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28",
               "User-Agent": "s2script-release"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if data is not None:
        headers["Content-Type"] = "application/octet-stream"
    req = urllib.request.Request(url, headers=headers, data=data)
    with urllib.request.build_opener(NoRedirect()).open(req, timeout=120) as response:
        return json.load(response)


def event():
    data = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    if data.get("action") != "published" or data.get("repository", {}).get("full_name") != REPO:
        raise RuntimeError("Only the canonical repository's release:published event is allowed")
    release = data["release"]
    tag = release["tag_name"]
    # Match internal/releasecontract.ParseVersion before running expensive builds.
    match = re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?", tag)
    if not match or len(tag) > 129 or any(
        part.isdigit() and len(part) > 1 and part[0] == "0"
        for part in (match[4] or "").split(".")
    ):
        raise RuntimeError("Release tag must be canonical v-prefixed SemVer, e.g. v0.0.1-alpha.1 (no dot after v, leading zeroes or build metadata)")
    if release["draft"]:
        raise RuntimeError("Draft releases cannot be published")
    if release["prerelease"] != bool(match[4]):
        raise RuntimeError("Release prerelease flag must match the tag: mark alpha/beta/rc tags as prerelease; leave stable versions unchecked")
    if not isinstance(release["id"], int) or release["id"] <= 0:
        raise RuntimeError("Invalid release ID")
    return release


def validate_identity(release, manifest):
    if release["tag_name"] != manifest["tag"] or bool(release["prerelease"]) != (manifest["channel"] == "prerelease"):
        raise RuntimeError("Release tag/prerelease flag differs from manifest")
    revision = run("git", "rev-parse", "--verify", "refs/tags/" + manifest["tag"] + "^{commit}")
    head = run("git", "rev-parse", "--verify", "HEAD^{commit}")
    if revision != head or revision != manifest["source"]["revision"]:
        raise RuntimeError("Tag, checked-out source and manifest revision differ")


def assets(release, token=""):
    result = request(API + "/releases/" + str(release["id"]) + "/assets?per_page=100", token)
    if len(result) >= 100:
        raise RuntimeError("Unexpected release asset count")
    return result


def upload(release, path, token):
    # GitHub returns 422 for duplicate names; never delete or clobber an asset.
    url = "https://uploads.github.com/repos/" + REPO + "/releases/" + str(release["id"]) + "/assets?name=" + urllib.parse.quote(path.name)
    return request(url, token, path.read_bytes())


def remote_image(tag, missing=False):
    ref = IMAGE + ":" + tag
    p = subprocess.run(["docker", "buildx", "imagetools", "inspect", ref, "--format", "{{json .Manifest}}"], capture_output=True, text=True)
    if p.returncode:
        message = p.stderr.lower()
        if missing and not any(s in message for s in ("unauthorized", "denied", "forbidden")) and any(s in message for s in ("manifest unknown", "not found")):
            return None
        raise RuntimeError("Cannot inspect registry identity for " + ref + "; check package access and registry availability")
    manifest = json.loads(p.stdout)
    digest = manifest.get("digest", "")
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", digest):
        raise RuntimeError("Registry did not return a canonical manifest digest")
    config = json.loads(run("docker", "buildx", "imagetools", "inspect", IMAGE + "@" + digest, "--format", "{{json .Image}}"))
    # This project publishes only a single Linux amd64 manifest, without attestations.
    if config.get("os") != "linux" or config.get("architecture") != "amd64":
        raise RuntimeError("Unsupported registry image platform")
    labels = config.get("config", {}).get("Labels", {})
    version, revision = labels.get("org.opencontainers.image.version"), labels.get("org.opencontainers.image.revision")
    if labels.get("org.opencontainers.image.source") != SOURCE or labels.get("org.opencontainers.image.licenses") != "GPL-3.0-or-later":
        raise RuntimeError("Registry image source/license identity unavailable")
    if not isinstance(version, str) or not isinstance(revision, str):
        raise RuntimeError("Registry image build identity unavailable")
    expected_source = SOURCE + "/releases/download/v" + version + "/s2script-cs2-egg-v" + version + "-source.tar.gz"
    if labels.get("org.s2script.corresponding-source") != expected_source:
        raise RuntimeError("Registry image corresponding source mismatch")
    return {"version": version, "revision": revision, "digest": digest}


def download(asset, destination):
    expected = SOURCE + "/releases/download/" + event()["tag_name"] + "/" + asset["name"]
    if asset["browser_download_url"] != expected or asset["state"] != "uploaded":
        raise RuntimeError("Unexpected public asset URL/state")
    if not isinstance(asset["size"], int) or not 0 < asset["size"] <= 1 << 30 or not re.fullmatch(r"sha256:[a-f0-9]{64}", asset.get("digest") or ""):
        raise RuntimeError("Missing public publisher integrity evidence")
    # No authorization, cookies or saved sessions; HTTPS redirects cannot carry credentials.
    class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            if urllib.parse.urlparse(newurl).scheme != "https":
                raise RuntimeError("Non-HTTPS asset redirect")
            return super().redirect_request(req, fp, code, msg, headers, newurl)
    h = hashlib.sha256()
    total = 0
    with urllib.request.build_opener(HTTPSRedirect()).open(expected, timeout=120) as response, destination.open("xb") as output:
        while chunk := response.read(1 << 20):
            total += len(chunk)
            if total > asset["size"]:
                raise RuntimeError("Public asset exceeded declared size")
            h.update(chunk)
            output.write(chunk)
    if total != asset["size"] or "sha256:" + h.hexdigest() != asset["digest"]:
        raise RuntimeError("Anonymous asset integrity mismatch")


def publish(directory, ctl):
    release = event()
    token = os.environ["GH_TOKEN"]
    repo = request(API, token)
    if repo.get("private") or repo.get("visibility") != "public":
        raise RuntimeError("Public corresponding source is required. An operator must review repository visibility; this workflow never changes it.")
    manifest = json.loads(run(ctl, "verify", "--output", str(directory), "--tag", release["tag_name"]))
    validate_identity(release, manifest)
    existing = assets(release, token)
    if existing:
        raise RuntimeError("Release already has assets. Publication is no-clobber; inspect partial state and follow docs/releases.md.")
    for suffix in ("", "-debug"):
        if remote_image(manifest["tag"] + suffix, missing=True) is not None:
            raise RuntimeError("Immutable image tag already exists; explicit repair required")
    for path in sorted(directory.iterdir()):
        upload(release, path, token)
    with tempfile.TemporaryDirectory() as scratch:
        api_file = Path(scratch) / "assets.json"
        api_file.write_text(json.dumps(assets(release, token)))
        run(ctl, "verify", "--output", str(directory), "--assets", str(api_file), "--tag", manifest["tag"])
        # Verify source anonymously before publishing images, not just via authenticated API metadata.
        source = next(a for a in assets(release, token) if a["name"] == manifest["source"]["archive"]["name"])
        download(source, Path(scratch) / "source.tar.gz")
        images = {}
        for target, suffix in (("runtime", ""), ("debug", "-debug")):
            metadata = Path(scratch) / (target + ".json")
            subprocess.run(["docker", "buildx", "build", "--platform", "linux/amd64", "--target", target,
                "--file", "docker/Dockerfile", "--provenance=false", "--sbom=false", "--push",
                "--tag", IMAGE + ":" + manifest["tag"] + suffix, "--metadata-file", str(metadata),
                "--build-arg", "VERSION=" + manifest["version"], "--build-arg", "REVISION=" + manifest["source"]["revision"],
                "--build-arg", "SOURCE_ARCHIVE=" + manifest["source"]["archive"]["url"], "."], check=True)
            digest = json.loads(metadata.read_text())["containerimage.digest"]
            expected = {"version": manifest["version"], "revision": manifest["source"]["revision"], "digest": digest}
            if remote_image(manifest["tag"] + suffix) != expected:
                raise RuntimeError("Pushed image identity differs from build receipt")
            images[target] = expected
        receipt = Path(scratch) / RECEIPT
        receipt.write_text(json.dumps({"schema_version": 1, "tag": manifest["tag"], "revision": manifest["source"]["revision"], "images": images}, sort_keys=True) + "\n")
        uploaded = upload(release, receipt, token)
        if uploaded.get("digest") != "sha256:" + hashlib.sha256(receipt.read_bytes()).hexdigest() or uploaded.get("size") != receipt.stat().st_size:
            raise RuntimeError("Publisher did not confirm image receipt integrity")
        for target, suffix in (("runtime", ""), ("debug", "-debug")):
            candidate = images[target]
            for channel in ("stable", "prerelease"):
                old = remote_image(channel + suffix, missing=True)
                args = [ctl, "alias", "--channel", channel, "--candidate", json.dumps(candidate)]
                if old is not None:
                    args += ["--current", json.dumps(old)]
                if run(*args) == "true":
                    subprocess.run(["docker", "buildx", "imagetools", "create", "--prefer-index=false", "--tag", IMAGE + ":" + channel + suffix, IMAGE + "@" + candidate["digest"]], check=True)
                    if remote_image(channel + suffix) != candidate:
                        raise RuntimeError("Alias promotion did not preserve image identity")


def public_verify(ctl):
    release = event()
    if os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN") or os.environ.get("DOCKER_AUTH_CONFIG"):
        raise RuntimeError("Public verification must have no authentication environment")
    config = Path(os.environ["DOCKER_CONFIG"])
    if config.exists() and any(config.iterdir()):
        raise RuntimeError("Public verification requires an empty Docker config")
    config.mkdir(parents=True, exist_ok=True)
    repo = request(API)
    if repo.get("private"):
        raise RuntimeError("Corresponding source is not public")
    with tempfile.TemporaryDirectory() as scratch:
        directory = Path(scratch)
        published = assets(release)
        by_name = {a["name"]: a for a in published}
        if len(by_name) != len(published):
            raise RuntimeError("Duplicate release assets")
        download(by_name[MANIFEST], directory / MANIFEST)
        # Local Go validator validates the untrusted manifest before selecting assets.
        manifest = json.loads((directory / MANIFEST).read_text())
        # Fixed exact filenames, never paths or commands from release metadata.
        tag = release["tag_name"]
        names = [f"s2node-{tag}-linux-amd64", f"s2egg-{tag}-linux-amd64", f"s2script-cs2-egg-{tag}-source.tar.gz",
                 f"s2script-cs2-egg-{tag}-deployment.tar.gz", "egg-s2script-cs2.json", "SHA256SUMS", RECEIPT]
        for name in names:
            download(by_name[name], directory / name)
        api_file = directory / "publisher.json"
        api_file.write_text(json.dumps(published))
        manifest = json.loads(run(ctl, "verify", "--output", str(directory), "--assets", str(api_file), "--tag", tag))
        validate_identity(release, manifest)
        receipt = json.loads((directory / RECEIPT).read_text())
        if receipt.get("schema_version") != 1 or receipt.get("tag") != tag or receipt.get("revision") != manifest["source"]["revision"] or set(receipt.get("images", {})) != {"runtime", "debug"}:
            raise RuntimeError("Invalid immutable image receipt")
        for target, suffix in (("runtime", ""), ("debug", "-debug")):
            expected = receipt["images"][target]
            if expected.get("version") != manifest["version"] or expected.get("revision") != manifest["source"]["revision"]:
                raise RuntimeError("Image receipt differs from source release")
            if remote_image(tag + suffix) != expected:
                raise RuntimeError("Anonymous immutable image identity mismatch")
        print("Anonymous source, release assets and both immutable images verified.")


if __name__ == "__main__":
    try:
        mode = sys.argv[1]
        if mode == "event":
            print(event()["tag_name"])
        elif mode == "publish":
            publish(Path(sys.argv[2]), sys.argv[3])
        elif mode == "public-verify":
            public_verify(sys.argv[2])
        else:
            raise RuntimeError("Unknown release operation")
    except Exception as exc:
        print("Release stopped:", exc, file=sys.stderr)
        print("Inspect partial publication; never overwrite immutable assets. If only public verification failed, review GHCR package visibility and rerun ONLY that read-only job. See docs/releases.md.", file=sys.stderr)
        sys.exit(1)
