# SPDX-License-Identifier: GPL-3.0-or-later
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

path = Path(__file__).resolve().parents[2] / "scripts/release.py"
spec = importlib.util.spec_from_file_location("publisher", path)
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)


class PublisherTests(unittest.TestCase):
    def event_file(self, directory, **changes):
        data = {"action": "published", "repository": {"full_name": publisher.REPO},
                "release": {"id": 1, "draft": False, "prerelease": True, "tag_name": "v0.1.0-alpha.1"}}
        data.update(changes)
        p = Path(directory) / "event.json"
        p.write_text(json.dumps(data))
        return p

    def test_only_explicit_canonical_release_event(self):
        with tempfile.TemporaryDirectory() as d:
            p = self.event_file(d)
            with patch.dict(os.environ, {"GITHUB_EVENT_PATH": str(p)}):
                self.assertEqual(publisher.event()["tag_name"], "v0.1.0-alpha.1")
                self.event_file(d, action="created")
                with self.assertRaises(RuntimeError):
                    publisher.event()
                self.event_file(d, repository={"full_name": "fork/repository"})
                with self.assertRaises(RuntimeError):
                    publisher.event()

    def test_event_rejects_noncanonical_tags_before_packaging(self):
        tags = ("v.0.0.1-alpha.1", "v01.2.3", "v1.2", "v1.2.3-alpha.01",
                "v1.2.3-", "v1.2.3-alpha..1", "v1.2.3+build", "1.2.3", "v1.2.3-" + "a" * 123)
        with tempfile.TemporaryDirectory() as d:
            for tag in tags:
                with self.subTest(tag=tag):
                    p = self.event_file(d, release={"id": 1, "draft": False, "prerelease": True, "tag_name": tag})
                    with patch.dict(os.environ, {"GITHUB_EVENT_PATH": str(p)}):
                        with self.assertRaisesRegex(RuntimeError, "canonical.*v0.0.1-alpha.1"):
                            publisher.event()

    def test_event_requires_matching_prerelease_flag(self):
        with tempfile.TemporaryDirectory() as d:
            for tag, prerelease in (("v0.0.1-alpha.1", False), ("v1.2.3", True)):
                with self.subTest(tag=tag):
                    p = self.event_file(d, release={"id": 1, "draft": False, "prerelease": prerelease, "tag_name": tag})
                    with patch.dict(os.environ, {"GITHUB_EVENT_PATH": str(p)}):
                        with self.assertRaisesRegex(RuntimeError, "prerelease"):
                            publisher.event()

    def test_event_accepts_canonical_stable_and_prerelease_tags(self):
        with tempfile.TemporaryDirectory() as d:
            for tag, prerelease in (("v0.0.1-alpha.1", True), ("v1.2.3", False), ("v1.2.3-0.alpha-01", True)):
                with self.subTest(tag=tag):
                    p = self.event_file(d, release={"id": 1, "draft": False, "prerelease": prerelease, "tag_name": tag})
                    with patch.dict(os.environ, {"GITHUB_EVENT_PATH": str(p)}):
                        self.assertEqual(publisher.event()["tag_name"], tag)

    def test_private_source_stops_before_upload_or_registry(self):
        with tempfile.TemporaryDirectory() as d:
            p = self.event_file(d)
            with patch.dict(os.environ, {"GITHUB_EVENT_PATH": str(p), "GH_TOKEN": "fixture"}), patch.object(publisher, "request", return_value={"private": True}), patch.object(publisher, "upload") as upload, patch.object(publisher, "remote_image") as registry:
                with self.assertRaisesRegex(RuntimeError, "Public corresponding source"):
                    publisher.publish(Path(d), "fixture")
                upload.assert_not_called()
                registry.assert_not_called()

    def test_public_verification_refuses_credentials(self):
        with tempfile.TemporaryDirectory() as d:
            p = self.event_file(d)
            with patch.dict(os.environ, {"GITHUB_EVENT_PATH": str(p), "GH_TOKEN": "fixture"}), patch.object(publisher, "request") as request:
                with self.assertRaisesRegex(RuntimeError, "no authentication"):
                    publisher.public_verify("fixture")
                request.assert_not_called()

    def test_prerelease_and_source_identity_must_match(self):
        release = {"tag_name": "v1.0.0", "prerelease": False}
        manifest = {"tag": "v1.0.0", "channel": "stable", "source": {"revision": "a" * 40}}
        with patch.object(publisher, "run", return_value="a" * 40):
            publisher.validate_identity(release, manifest)
            with self.assertRaises(RuntimeError):
                publisher.validate_identity(dict(release, prerelease=True), manifest)
        with patch.object(publisher, "run", side_effect=["a" * 40, "b" * 40]):
            with self.assertRaises(RuntimeError):
                publisher.validate_identity(release, manifest)

    def test_auth_failure_is_never_an_absent_tag(self):
        result = type("Result", (), {"returncode": 1, "stderr": "unauthorized: not found"})()
        with patch.object(publisher.subprocess, "run", return_value=result):
            with self.assertRaises(RuntimeError):
                publisher.remote_image("v1.0.0", missing=True)
        result.stderr = "manifest unknown"
        with patch.object(publisher.subprocess, "run", return_value=result):
            self.assertIsNone(publisher.remote_image("v1.0.0", missing=True))
