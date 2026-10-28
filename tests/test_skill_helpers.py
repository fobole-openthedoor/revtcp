import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import tempfile
import unittest
from unittest import mock
import urllib.error
import zipfile


ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "skills/revtcp-deploy/scripts"


def module(name):
    spec = importlib.util.spec_from_file_location(name, SCRIPTS / (name + ".py"))
    loaded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(loaded)
    return loaded


class Response(io.BytesIO):
    def __init__(self, status, payload=None, headers=None):
        super().__init__(json.dumps(payload or {}).encode())
        self.status, self.headers = status, headers or {}


class SkillHelpersTest(unittest.TestCase):
    def test_bundles_are_isolated_and_contain_links_without_other_private_keys(self):
        packager = module("package_client")
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            certs = root / "pki"
            certs.mkdir()
            for name in ("ca.crt", "client.crt", "client.key", "ca.key", "server.key"):
                (certs / name).write_text("test fixture: " + name)
            labels = []
            for public_port in (18801, 18802):
                args = ["package_client.py", "--project-root", str(ROOT), "--cert-dir", str(certs), "--host", "2001:db8::1", "--tunnel-port", str(public_port + 100), "--public-port", str(public_port), "--target", "[::1]:6768", "--release-tag", "v0.1.0", "--output-dir", str(root / "out")]
                with mock.patch("sys.argv", args), contextlib.redirect_stdout(io.StringIO()):
                    packager.main()
                archive = root / "out" / f"revtcp-client-{public_port}.zip"
                self.assertEqual(archive.stat().st_mode & 0o777, 0o600)
                with zipfile.ZipFile(archive) as z:
                    prefix = f"revtcp-client-{public_port}/"
                    names = set(z.namelist())
                    self.assertEqual({n for n in names if n.endswith(".key")}, {prefix + "certs/client.key"})
                    config = json.loads(z.read(prefix + "client.json"))
                    self.assertEqual(config["target"], "[::1]:6768")
                    self.assertEqual(config["server"], f"[2001:db8::1]:{public_port + 100}")
                    info = json.loads(z.read(prefix + "bundle-info.json"))
                    labels.append(info["macos_label"])
                    links = z.read(prefix + "LINKS.txt").decode()
                    self.assertIn(info["project_url"], links)
                    self.assertTrue(all(url in links for url in info["downloads"].values()))
                    plist = plistlib.loads(z.read(prefix + info["macos_label"] + ".plist"))
                    self.assertEqual(plist["Label"], info["macos_label"])
                    self.assertEqual(plist["ProgramArguments"][3], f"/Library/Application Support/revtcp-{public_port}/config/client.json")
                original = hashlib.sha256(archive.read_bytes()).digest()
                with mock.patch("sys.argv", args), contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                    packager.main()
                self.assertEqual(hashlib.sha256(archive.read_bytes()).digest(), original)
            self.assertNotEqual(labels[0], labels[1])

    def test_upload_recovers_without_leaking_credentials_or_recreating_completed_file(self):
        uploader = module("upload_bundle")
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "bundle.zip"
            content = b"small upload content"
            source.write_bytes(content)
            credentials = root / "credentials.json"
            credentials.write_text(json.dumps({"base_url": uploader.BASE, "api_key": "dummy-management-secret"}))
            credentials.chmod(0o600)
            state = root / "receipt.json"
            counts = {"create": 0, "google": 0, "complete": 0}
            expired = False
            upload_id = None

            def open_request(req, timeout):
                nonlocal upload_id
                url = req.full_url
                authorization = req.get_header("Authorization")
                if "/agent-docs/version?" in url:
                    self.assertIsNone(authorization)
                    return Response(200, {"update_available": False})
                if url == uploader.BASE + "/v1/uploads":
                    counts["create"] += 1
                    self.assertEqual(authorization, "Bearer dummy-management-secret")
                    upload_id = req.get_header("Idempotency-key")
                    return Response(201, {"upload_id": upload_id, "upload_url": "https://www.googleapis.com/upload/test", "upload_token": "dummy-upload-secret", "complete_url": uploader.BASE + f"/v1/uploads/{upload_id}/complete", "share": None})
                if url.startswith("https://www.googleapis.com/"):
                    counts["google"] += 1
                    self.assertIsNone(authorization)
                    if counts["google"] == 1:
                        raise urllib.error.URLError("response lost after partial upload")
                    if counts["google"] == 2:
                        self.assertEqual(req.data, b"")
                        self.assertEqual(req.get_header("Content-range"), f"bytes */{len(content)}")
                        return Response(308, headers={"Range": "bytes=0-2"})
                    self.assertEqual(req.data, content[3:])
                    self.assertEqual(req.get_header("Content-range"), f"bytes 3-{len(content)-1}/{len(content)}")
                    return Response(201)
                if url.endswith("/complete"):
                    counts["complete"] += 1
                    self.assertEqual(authorization, "Bearer dummy-upload-secret")
                    if expired:
                        return Response(410)
                    return Response(200, {"status": "ready", "mode": "redirect", "share_url": uploader.BASE + "/s/example", "file_expires_at": 2000000000})
                if url.endswith("/s/example"):
                    self.assertIsNone(authorization)
                    return Response(302, headers={"Location": "https://drive.google.com/example"})
                self.fail("unexpected HTTP destination")

            args = ["upload_bundle.py", str(source), "--state", str(state), "--credentials", str(credentials)]
            with mock.patch("sys.argv", args), mock.patch.dict(os.environ, {"GDRIVE_API_TOKEN": "", "GDRIVE_PUBLIC_URL": ""}), mock.patch.object(uploader.urllib.request, "build_opener", return_value=mock.Mock(open=open_request)), mock.patch.object(uploader.time, "sleep"):
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    uploader.main()
                self.assertTrue(json.loads(output.getvalue())["direct_download_verified"])
                self.assertEqual(counts["google"], 3)
                self.assertNotIn("dummy-management-secret", state.read_text())
                self.assertEqual(state.stat().st_mode & 0o777, 0o600)
                with contextlib.redirect_stdout(io.StringIO()):
                    uploader.main()
                self.assertEqual(counts["create"], 1)
                self.assertEqual(counts["google"], 3)
                expired = True
                with self.assertRaises(uploader.UploadError):
                    uploader.main()
                self.assertEqual(counts["create"], 1)


if __name__ == "__main__":
    unittest.main()
