# mobile/scripts — Apple + Google store automation

API-driven replacements for the manual Apple Developer Portal /
App Store Connect / Play Console clicks we worked through during
Block 76Q's TestFlight + Play Internal bring-up (2026-05-14). The
goal is that after this directory lands, **no future build,
release, tester, or capability change requires a browser session**.

## Why APIs, not browser automation

We initially considered driving the App Store Connect / Play Console
UI via Playwright. Reality:

- **Apple's auth flow** requires Apple ID + 2FA-to-iPhone on every
  fresh browser session. Playwright contexts don't share cookies
  with the operator's existing browser; you'd need to re-auth in
  the controlled browser every time, and Apple's anti-automation
  heuristics frequently break the click path mid-flow.
- **Google's** is more permissive but Play Console's React UI has
  enough dynamic ids that selector drift breaks scripts on each
  Play Console redesign.
- **Both companies have first-class REST APIs** that cover every
  click path we ran into. Apple's App Store Connect API has been
  stable since 2018; Google's Play Developer Publishing API since
  2014. Both are versioned + deprecated cleanly. They're the right
  surface.

## Auth

`.envrc.local` at the repo root carries the credentials (direnv-managed,
gitignored). Required env vars per platform:

### Apple (App Store Connect API)

```
APPLE_DEVELOPMENT_TEAM    # 10-char Team ID (e.g. GSJCWXC8E4)
ASC_API_KEY_ID            # 10-char key ID
ASC_API_ISSUER_ID         # UUID
ASC_API_KEY_PATH          # path to AuthKey_<KEY_ID>.p8
```

The `.p8` private key is the ECDSA P-256 key Apple gave you on key
creation. Generate at
[appstoreconnect.apple.com/access/integrations/api](https://appstoreconnect.apple.com/access/integrations/api).
**Role required: Admin** for full capability management; App Manager
suffices for build/upload-only flows.

### Google (Play Developer Publishing API)

```
GOOGLE_PLAY_SERVICE_ACCOUNT_KEY   # path to service-account JSON
ANDROID_PACKAGE_NAME              # io.dlf_dds.goat_client
```

Service account is created in Play Console → Setup → API access →
Create new service account. JSON key downloads once. Grant the
service account access to your app under the same API access page.
**No equivalent to Apple's "role" — Play grants per-app permission
checkboxes (release manager / app manager / etc).**

## Manual-click → API mapping

Every action we did manually during 2026-05-14 bring-up, and the
API endpoint that does it instead.

### Apple Developer Portal + App Store Connect

| Manual step                                         | API endpoint                                          | Script |
|-----------------------------------------------------|-------------------------------------------------------|--------|
| Register App ID (`io.dlf-dds.goat-client`)          | `POST /v1/bundleIds`                                  | `apple/setup_app_ids.py` |
| Enable Network Extensions capability                | `POST /v1/bundleIdCapabilities`                       | `apple/setup_app_ids.py` |
| Enable App Groups capability + link group           | `POST /v1/bundleIdCapabilities` (+ link)              | `apple/setup_app_ids.py` |
| Create App Group `group.io.dlf-dds.goat-client`     | `POST /v1/appGroups`                                  | `apple/setup_app_ids.py` |
| Generate Distribution certificate                   | `POST /v1/certificates` (type `DISTRIBUTION`)         | (cloud signing handles) |
| Generate provisioning profile                       | `POST /v1/profiles`                                   | (cloud signing handles) |
| Create app in App Store Connect (New App)           | `POST /v1/apps`                                       | `apple/setup_app_ids.py` |
| Upload IPA to TestFlight                            | `altool --upload-app` (no direct API)                 | `apple/upload_to_testflight.py` |
| Poll build processing status                        | `GET /v1/builds?filter[app]=<id>`                     | `apple/upload_to_testflight.py` |
| Create internal tester group                        | `POST /v1/betaGroups` (with `hasAccessToAllBuilds=true` at create-time — can't be patched later) | `apple/manage_testers.py` |
| Add build to tester group                           | `POST /v1/betaGroups/<id>/relationships/builds`       | `apple/manage_testers.py` |
| Submit Export Compliance answer                     | `PATCH /v1/builds/<id>` w/ `usesNonExemptEncryption`  | `apple/manage_testers.py` |
| List provisioning profiles                          | `GET /v1/profiles`                                    | (debug only) |
| Delete stale provisioning profile                   | `DELETE /v1/profiles/<id>`                            | (debug only) |
| **Add a team User as an internal beta tester**      | **NO API** — UI-only                                  | **see "Known API gaps" below** |
| Add external tester by email                        | `POST /v1/betaTesters` (creates record + invites by email) | `apple/manage_testers.py` (external mode) |

Reference: [developer.apple.com/documentation/appstoreconnectapi](https://developer.apple.com/documentation/appstoreconnectapi).

### Google Play Console

| Manual step                                  | API method                                            | Script |
|----------------------------------------------|-------------------------------------------------------|--------|
| Create app listing                           | `androidpublisher.applications.create` (limited; usually one-shot via Console) | (do once) |
| Upload AAB to Internal Testing track         | `androidpublisher.edits.bundles.upload` + commit edit | `google/upload_to_play_internal.py` |
| Set release notes                            | `androidpublisher.edits.tracks.update`                | `google/upload_to_play_internal.py` |
| Promote release to Internal track            | `androidpublisher.edits.tracks.update`                | `google/upload_to_play_internal.py` |
| Add tester email                             | Email is added to a Play Console "tester list"; API: `androidpublisher.edits.tracks.update` with `testers.googleGroups` or `testers.emails` | `google/manage_testers.py` |
| Get Internal track opt-in URL                | Constructed: `https://play.google.com/apps/internaltest/<app_id>` | (no API; URL is fixed) |
| Promote to Production                        | `androidpublisher.edits.tracks.update` (track `production`) | not in v0.2 scope |

Reference: [developers.google.com/android-publisher](https://developers.google.com/android-publisher).

## Scripts

Each script is a standalone CLI; they share `apple/asc_api.py` and
`google/play_api.py` as auth + HTTP libraries.

```
mobile/scripts/
├── README.md                          # this file
├── requirements.txt                   # pip install -r requirements.txt
├── apple/
│   ├── asc_api.py                     # ASC API JWT client + HTTP wrapper
│   ├── setup_app_ids.py               # idempotent App ID + caps + group setup
│   ├── upload_to_testflight.py        # archive + altool upload + compliance
│   └── manage_testers.py              # beta groups + testers + build assignment
└── google/
    ├── play_api.py                    # Play Developer API client wrapper
    ├── upload_to_play_internal.py     # AAB upload + release notes + rollout
    └── manage_testers.py              # tester email list management
```

Run any script with `--help` for its CLI surface.

## Quickstart

```bash
# One-time per fresh checkout:
python3 -m venv mobile/scripts/.venv
source mobile/scripts/.venv/bin/activate
pip install -r mobile/scripts/requirements.txt

# Direnv loads .envrc.local automatically. If not using direnv:
source .envrc.local

# Idempotently ensure Apple Dev Portal state matches what the goat-client
# build expects (App IDs + caps + groups). Safe to re-run.
python mobile/scripts/apple/setup_app_ids.py

# Build + upload + assign to internal testers, all in one go:
python mobile/scripts/apple/upload_to_testflight.py
python mobile/scripts/apple/manage_testers.py add --email you@example.com

# Same for Android:
python mobile/scripts/google/upload_to_play_internal.py --aab path/to/app-release.aab
python mobile/scripts/google/manage_testers.py add --email benschiff1@gmail.com
```

## Known API gaps (UI-only steps)

Discovered while writing this toolkit. Both confirmed against the live
APIs as of 2026-05-14:

### Apple — adding a team User as an internal beta tester

Apple's ASC API does **not** expose a way to enrol an existing team
user (User record) as a tester in an internal beta group. The
relationships `/v1/users/{id}/relationships/betaGroups` and
`/v1/betaGroups/{id}/relationships/users` both return
`404 The relationship 'X' does not exist`. The `POST /v1/betaTesters`
endpoint refuses with `409` because the email already belongs to a
User on the team.

**Workaround**: one click in App Store Connect →
TestFlight → Internal Group → Testers tab → **+** → select the team
member from the dropdown → Save. Then the team user sees TestFlight
builds normally.

The toolkit creates beta groups with `hasAccessToAllBuilds=true` so
that once any team user IS enrolled (even via the UI), they see every
build automatically — no per-build re-attachment needed.

### Apple — Beta App Review submission for external testers

External testers (any email, doesn't need team-User status) require
Apple's Beta App Review for the first build per app. The API exposes
`POST /v1/betaAppReviewSubmissions` to submit a build for review, but
the review itself is human-driven on Apple's side (24h-ish SLA in
practice). After the first build passes review, subsequent builds
usually auto-approve.

### Google — Play Console initial app listing

The first creation of the Play Console listing
(`Create app → name + language + free/paid + declarations`) is UI-only
for new apps. Once the listing exists, subsequent metadata + track
updates are all via `androidpublisher` API. Single one-time UI action
per app.

### Google — Play Console "device verification" (Gate B)

Google's tester verification for newly-created Personal developer
accounts requires installing the Play Console mobile app on a real
Android device. One-time. Not scriptable.

## What this does NOT replace

- **Initial dev-program enrollment** ($99 Apple, $25 Google one-time).
  These ARE browser flows because they involve identity verification
  + payment. One-time only; we did them on 2026-05-14.
- **Apple's "device verification" gate** for new Play Console
  developer accounts (Gate B). Done via the Play Console mobile app
  on a physical Android device; not scriptable. One-time only.
- **Code-signing key generation**. `keytool` (Android upload key)
  and Xcode cloud-signing-cert generation are local-machine actions.
  We did this on 2026-05-14; keystore lives at
  `~/.android/goat-client-upload.keystore`.
- **Service-account JSON generation** for Play Developer API. Done
  once in Play Console → Setup → API access. JSON file lives outside
  the repo, path in `.envrc.local`.
