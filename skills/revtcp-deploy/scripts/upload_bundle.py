#!/usr/bin/env python3
"""Upload one authorized revtcp bundle to SM中转 and return a verified redirect link."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


BASE = "https://upload.sm.sb"
TRANSIENT = (429, 500, 502, 503, 504)


class UploadError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("file", type=Path)
    parser.add_argument("--state", type=Path, required=True, help="private receipt/resume file outside the repository")
    parser.add_argument("--credentials", type=Path, default=Path.home() / ".config/gdrive-bridge/credentials.json")
    parser.add_argument("--retention-days", type=int, default=30)
    args = parser.parse_args()
    if not 1 <= args.retention_days <= 365000:
        parser.error("retention-days must be 1..365000")
    os.umask(0o077)
    source = args.file.resolve()
    if not source.is_file() or source.stat().st_size <= 0:
        raise UploadError("upload file is missing or empty")
    hasher = hashlib.sha256()
    with source.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            hasher.update(chunk)
    metadata = {"name": source.name, "size": source.stat().st_size, "mime_type": "application/zip", "mode": "redirect", "retention_days": args.retention_days}
    if args.state.exists():
        state = json.loads(args.state.read_text())
        if state.get("base_url") != BASE or state.get("sha256") != hasher.hexdigest() or state.get("metadata") != metadata:
            raise UploadError("state belongs to a different file/site/retention; use a new receipt for an authorized new upload")
    else:
        state = {"base_url": BASE, "upload_id": str(uuid.uuid4()), "source": str(source), "sha256": hasher.hexdigest(), "metadata": metadata}
    if os.environ.get("GDRIVE_API_TOKEN"):
        if os.environ.get("GDRIVE_PUBLIC_URL", "").rstrip("/") != BASE:
            raise UploadError("GDRIVE_PUBLIC_URL must match the upload site before using its environment token")
        token = os.environ["GDRIVE_API_TOKEN"]
    else:
        if args.credentials.stat().st_mode & 0o077:
            raise UploadError("credentials file must have mode 0600")
        credentials = json.loads(args.credentials.read_text())
        if credentials.get("base_url", "").rstrip("/") != BASE:
            raise UploadError("credentials belong to a different site")
        token = credentials.get("api_key")
        if not token:
            raise UploadError("upload API key is missing")
    args.state.parent.mkdir(parents=True, exist_ok=True, mode=0o700)

    def save():
        temporary = args.state.with_name(args.state.name + ".tmp")
        temporary.write_text(json.dumps(state))
        temporary.chmod(0o600)
        temporary.replace(args.state)

    save()
    opener = urllib.request.build_opener(NoRedirect)

    def request(method, url, body=None, headers=None):
        headers = {"User-Agent": "Mozilla/5.0", **(headers or {})}
        req = urllib.request.Request(url, data=body, method=method, headers=headers)
        try:
            with opener.open(req, timeout=30) as response:
                return response.status, response.headers, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.headers, error.read()

    def pause(attempt, headers=None):
        value = (headers or {}).get("Retry-After", "")
        delay = float(value) if value.isdigit() else min(2**attempt, 15)
        # Do not retry earlier than the server requested when the wait is long.
        if delay > 30:
            raise UploadError(f"server requested Retry-After {delay:g}s; resume this receipt later")
        time.sleep(delay)

    def api(method, path, body=None, key=None, extra=None, pending=False):
        if not path.startswith("/v1/"):
            raise UploadError("unexpected API path")
        headers = {"Accept": "application/json", **(extra or {})}
        if key:
            headers["Authorization"] = "Bearer " + key
        if body is not None:
            headers["Content-Type"] = "application/json"
            body = json.dumps(body).encode()
        for attempt in range(5):
            try:
                code, response_headers, payload = request(method, BASE + path, body, headers)
                if code in (200, 201):
                    result = json.loads(payload)
                    if not (pending and result.get("status") == "pending"):
                        return result
                elif code not in TRANSIENT:
                    raise UploadError(f"API {method} failed with HTTP {code}; upload_id={state['upload_id']}")
                pause(attempt, response_headers)
            except (urllib.error.URLError, TimeoutError):
                pause(attempt)
        raise UploadError(f"API attempts exhausted; upload_id={state['upload_id']}")

    # No credentials on documentation/version requests.
    versions = api("GET", "/v1/agent-docs/version?skill_version=2.1.0&api_version=2.2.0")
    if versions.get("update_available") is not False:
        raise UploadError("upload API documentation changed; read https://upload.sm.sb/agents before updating this helper")

    def create(restart=False):
        body = dict(metadata)
        if restart:
            body["restart"] = True
        result = api("POST", "/v1/uploads", body, token, {"Idempotency-Key": state["upload_id"]})
        if result.get("upload_id") != state["upload_id"]:
            raise UploadError("unexpected upload identifier")
        expected = BASE + "/v1/uploads/" + state["upload_id"] + "/complete"
        if result.get("complete_url") != expected:
            raise UploadError("unexpected completion destination")
        state["session"] = result
        save()
        return result

    fresh = "session" not in state
    session = create() if fresh else state["session"]
    share = session.get("share") or {}
    if share.get("status") in ("expired", "deleted"):
        raise UploadError("file expired/deleted; do not recreate it automatically")
    if not share and not state.get("google_complete"):
        offset, uncertain, failures, restarts = 0, not fresh, 0, 0
        while True:
            url = session["upload_url"]
            parsed = urllib.parse.urlsplit(url)
            if parsed.scheme != "https" or parsed.hostname != "www.googleapis.com" or parsed.username or parsed.password or parsed.port not in (None, 443):
                raise UploadError("unexpected Google upload destination")
            size = metadata["size"]
            if uncertain:
                body, content_range = b"", f"bytes */{size}"
            else:
                with source.open("rb") as file:
                    file.seek(offset)
                    body = file.read(min(8388608, size - offset))
                content_range = f"bytes {offset}-{offset + len(body) - 1}/{size}"
            try:
                # Separate request: never send the site key/upload_token to Google.
                code, headers, _ = request("PUT", url, body, {"Content-Type": metadata["mime_type"], "Content-Length": str(len(body)), "Content-Range": content_range})
                if code in (200, 201):
                    state["google_complete"] = True
                    save()
                    break
                if code == 308:
                    match = re.fullmatch(r"bytes=0-(\d+)", headers.get("Range", ""))
                    next_offset = int(match.group(1)) + 1 if match else 0
                    if next_offset < offset or next_offset > size:
                        raise UploadError("invalid Google upload offset")
                    failures = 0 if next_offset > offset else failures + 1
                    offset, uncertain = next_offset, next_offset == size
                elif code == 404 and restarts == 0:
                    session = create(restart=True)
                    restarts += 1
                    if session.get("share"):
                        break
                    offset, uncertain = 0, False
                elif code in TRANSIENT:
                    failures += 1
                    uncertain = True
                    pause(failures - 1, headers)
                else:
                    raise UploadError(f"Google upload failed with HTTP {code}; upload_id={state['upload_id']}")
            except (urllib.error.URLError, TimeoutError):
                failures += 1
                uncertain = True
                pause(failures - 1)
            if failures >= 5:
                raise UploadError(f"Google upload needs recovery; upload_id={state['upload_id']}")

    result = api("POST", f"/v1/uploads/{state['upload_id']}/complete", {}, session["upload_token"], pending=True)
    state["result"] = result
    save()
    if result.get("status") != "ready" or result.get("mode") != "redirect" or not result.get("share_url"):
        raise UploadError(f"upload is not ready for direct download; upload_id={state['upload_id']}")
    link = result["share_url"]
    parsed = urllib.parse.urlsplit(link)
    if parsed.scheme != "https" or parsed.netloc != "upload.sm.sb":
        raise UploadError("unexpected share link")
    # This endpoint returns 405 to HEAD; GET without following redirects checks
    # the direct link and can trigger the site's standard download notification.
    code, headers, _ = request("GET", link)
    if code not in (302, 303, 307) or not headers.get("Location"):
        raise UploadError(f"share exists but redirect verification failed: HTTP {code}")
    expiry = result.get("file_expires_at")
    print(json.dumps({"file": source.name, "share_url": link, "upload_id": state["upload_id"], "expires_at_utc": datetime.datetime.fromtimestamp(expiry, datetime.timezone.utc).isoformat() if expiry else None, "direct_download_verified": True}, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (UploadError, OSError, ValueError, KeyError) as error:
        message = str(error) if isinstance(error, UploadError) else type(error).__name__ + ": check local input/state or API schema"
        print(message, file=sys.stderr)
        sys.exit(1)
