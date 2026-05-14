"""App Store Connect API client.

Thin wrapper around the ASC v1 REST API. Reads credentials from env vars
populated by .envrc.local. Signs requests with a short-lived ES256 JWT
per Apple's auth spec.

Reference: https://developer.apple.com/documentation/appstoreconnectapi
"""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any

import jwt  # pyjwt


API_BASE = "https://api.appstoreconnect.apple.com"
JWT_AUDIENCE = "appstoreconnect-v1"
JWT_TTL_SECONDS = 600  # Apple max is 20 minutes; 10 keeps us well clear of clock skew.


class ASCError(RuntimeError):
    """An App Store Connect API error response. `errors` is the parsed JSON list."""

    def __init__(self, status: int, errors: list[dict[str, Any]] | None, raw: str):
        self.status = status
        self.errors = errors or []
        self.raw = raw
        if errors:
            messages = "; ".join(e.get("title", "") + ": " + e.get("detail", "") for e in errors)
            super().__init__(f"ASC API {status}: {messages}")
        else:
            super().__init__(f"ASC API {status}: {raw[:400]}")


def _required_env(name: str) -> str:
    v = os.environ.get(name, "").strip()
    if not v:
        sys.exit(
            f"error: env var {name} not set. Source the repo's .envrc.local "
            f"(or set it manually) before running this script."
        )
    return v


def _load_key(path: str) -> str:
    if not os.path.isfile(path):
        sys.exit(f"error: ASC_API_KEY_PATH={path} does not exist.")
    with open(path) as f:
        return f.read()


def make_jwt(key_id: str, issuer_id: str, key_pem: str) -> str:
    """Sign an App Store Connect API JWT (ES256, 10-min TTL).

    Apple validates: iss == issuer ID, exp ≤ now+20min, aud == appstoreconnect-v1,
    kid == key ID, alg == ES256.
    """
    now = int(time.time())
    payload = {
        "iss": issuer_id,
        "iat": now,
        "exp": now + JWT_TTL_SECONDS,
        "aud": JWT_AUDIENCE,
    }
    return jwt.encode(payload, key_pem, algorithm="ES256", headers={"kid": key_id, "typ": "JWT"})


class ASCClient:
    """Thin client over the ASC v1 REST API.

    All methods raise ``ASCError`` on non-2xx responses. JSON bodies are
    decoded automatically. The client refreshes the JWT every 8 minutes so
    long-running scripts (e.g. polling for build processing) don't expire.
    """

    def __init__(self, key_id: str | None = None, issuer_id: str | None = None, key_path: str | None = None):
        self.key_id = key_id or _required_env("ASC_API_KEY_ID")
        self.issuer_id = issuer_id or _required_env("ASC_API_ISSUER_ID")
        self.key_path = key_path or _required_env("ASC_API_KEY_PATH")
        self._key_pem = _load_key(self.key_path)
        self._token: str | None = None
        self._token_minted_at: float = 0

    def _token_fresh(self) -> str:
        if self._token is None or (time.time() - self._token_minted_at) > 480:
            self._token = make_jwt(self.key_id, self.issuer_id, self._key_pem)
            self._token_minted_at = time.time()
        return self._token

    def _request(
        self,
        method: str,
        path: str,
        params: dict[str, Any] | None = None,
        body: Any = None,
    ) -> dict[str, Any] | None:
        url = API_BASE + path
        if params:
            # ASC uses bracket-style filter params: filter[bundleId]=...
            url += "?" + urllib.parse.urlencode(params, doseq=True)
        data = None
        headers = {"Authorization": f"Bearer {self._token_fresh()}"}
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req) as resp:
                raw = resp.read().decode("utf-8", errors="replace")
                if not raw.strip():
                    return None
                return json.loads(raw)
        except urllib.error.HTTPError as e:
            raw = e.read().decode("utf-8", errors="replace") if e.fp else ""
            errors: list[dict[str, Any]] | None = None
            try:
                errors = json.loads(raw).get("errors")
            except Exception:
                pass
            raise ASCError(e.code, errors, raw)

    # ─── Public surface ────────────────────────────────────────────────────

    def get(self, path: str, **params: Any) -> dict[str, Any]:
        out = self._request("GET", path, params=params or None)
        return out or {}

    def post(self, path: str, body: Any) -> dict[str, Any] | None:
        return self._request("POST", path, body=body)

    def patch(self, path: str, body: Any) -> dict[str, Any] | None:
        return self._request("PATCH", path, body=body)

    def delete(self, path: str) -> None:
        self._request("DELETE", path)

    # ─── Higher-level helpers ─────────────────────────────────────────────

    def find_app(self, bundle_id: str) -> dict[str, Any] | None:
        """Look up an App Store Connect app record by bundle ID. None if missing."""
        # ASC's /v1/apps filter is by bundleId. Returns up to 200 records.
        resp = self.get("/v1/apps", **{"filter[bundleId]": bundle_id})
        for record in resp.get("data", []):
            if record.get("attributes", {}).get("bundleId") == bundle_id:
                return record
        return None

    def find_bundle_id(self, identifier: str) -> dict[str, Any] | None:
        """Look up a Developer Portal Bundle ID record. None if missing."""
        resp = self.get("/v1/bundleIds", **{"filter[identifier]": identifier})
        for record in resp.get("data", []):
            if record.get("attributes", {}).get("identifier") == identifier:
                return record
        return None

    def list_builds(self, app_id: str, limit: int = 20) -> list[dict[str, Any]]:
        """Return builds for an app in newest-first order, with state."""
        resp = self.get(
            "/v1/builds",
            **{
                "filter[app]": app_id,
                "limit": limit,
                "sort": "-uploadedDate",
                "fields[builds]": "version,uploadedDate,processingState,expired,usesNonExemptEncryption,buildAudienceType",
            },
        )
        return resp.get("data", [])

    def beta_groups(self, app_id: str) -> list[dict[str, Any]]:
        """List beta tester groups for an app."""
        resp = self.get(
            "/v1/betaGroups",
            **{"filter[app]": app_id, "limit": 50},
        )
        return resp.get("data", [])

    def create_beta_group(self, app_id: str, name: str, internal: bool = True) -> dict[str, Any]:
        """Create a beta tester group. Returns the created record."""
        body = {
            "data": {
                "type": "betaGroups",
                "attributes": {
                    "name": name,
                    "isInternalGroup": internal,
                    "publicLinkEnabled": False,
                },
                "relationships": {"app": {"data": {"type": "apps", "id": app_id}}},
            }
        }
        out = self.post("/v1/betaGroups", body)
        return (out or {}).get("data", {})

    def add_internal_tester(self, app_id: str, email: str, first_name: str = "Tester", last_name: str = "Goat") -> dict[str, Any] | None:
        """Invite a new internal tester by email.

        ASC requires the tester to already be a User on your team with
        access to the app. For internal testers this means the email
        must correspond to an Apple ID with team membership. External
        testers go through `betaTesters` directly.

        Returns the created BetaTester record, or None if the tester
        already exists (Apple returns 409 in that case).
        """
        body = {
            "data": {
                "type": "betaTesters",
                "attributes": {
                    "email": email,
                    "firstName": first_name,
                    "lastName": last_name,
                },
                "relationships": {
                    "apps": {"data": [{"type": "apps", "id": app_id}]},
                },
            }
        }
        try:
            out = self.post("/v1/betaTesters", body)
            return (out or {}).get("data", {})
        except ASCError as e:
            if e.status == 409:
                return None
            raise

    def attach_tester_to_group(self, group_id: str, tester_id: str) -> None:
        """Attach an existing BetaTester to a BetaGroup."""
        body = {"data": [{"type": "betaTesters", "id": tester_id}]}
        self.post(f"/v1/betaGroups/{group_id}/relationships/betaTesters", body)

    def attach_build_to_group(self, group_id: str, build_id: str) -> None:
        """Attach a Build to a BetaGroup so the group's testers can install it."""
        body = {"data": [{"type": "builds", "id": build_id}]}
        self.post(f"/v1/betaGroups/{group_id}/relationships/builds", body)

    def set_export_compliance(self, build_id: str, uses_non_exempt_encryption: bool) -> None:
        """Answer the Export Compliance question on a build.

        For apps that use only standard wireguard-go / TLS crypto, the
        Mass Market Open Source exemption applies — pass False.
        """
        body = {
            "data": {
                "type": "builds",
                "id": build_id,
                "attributes": {"usesNonExemptEncryption": uses_non_exempt_encryption},
            }
        }
        self.patch(f"/v1/builds/{build_id}", body)


def main_smoke():
    """Quick connectivity check: list apps for the team."""
    c = ASCClient()
    resp = c.get("/v1/apps", limit=5)
    for record in resp.get("data", []):
        attrs = record.get("attributes", {})
        print(f"  {record['id']}  {attrs.get('bundleId'):<40}  {attrs.get('name')}")


if __name__ == "__main__":
    main_smoke()
