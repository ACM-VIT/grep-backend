# grep-backend

The admin service behind [grep.acmvit.in](https://grep.acmvit.in) — the ACM-VIT
newsletter.

It does three things:

1. **Holds the editions** the website shows.
2. **Lets an approved Google account create and edit them**, at `/admin` on the
   website.
3. **Takes PDF uploads**, or accepts a link to a PDF already in a bucket.

It is one Go binary and two JSON files. There is no database to install, no
migration to run, and no queue. A newsletter publishes a few times a year.

---

## Contents

- [How the two pieces fit together](#how-the-two-pieces-fit-together)
- [Running it locally](#running-it-locally)
- [Creating the Google sign-in key](#creating-the-google-sign-in-key)
- [Who is allowed in](#who-is-allowed-in)
- [Publishing an edition](#publishing-an-edition)
- [Where the files live](#where-the-files-live)
  - [Option A — upload through the admin](#option-a--upload-through-the-admin)
  - [Option B — Google Cloud Storage](#option-b--google-cloud-storage)
  - [Option C — Cloudflare R2](#option-c--cloudflare-r2)
  - [Option D — Amazon S3](#option-d--amazon-s3)
- [Deploying](#deploying)
- [Backing it up](#backing-it-up)
- [Handing over](#handing-over)
- [The API](#the-api)
- [When something is wrong](#when-something-is-wrong)

---

## How the two pieces fit together

```
                     ┌──────────────────────────────┐
  a reader  ────────▶│  grep-website  (Astro, Node) │
                     │                              │
                     │  /            /editions      │
                     │  /read/:slug  /admin         │
                     └──────┬───────────────┬───────┘
                            │               │
        server-side fetch   │               │  browser fetch, with a
        of published        │               │  Google ID token
        editions            │               │
                            ▼               ▼
                     ┌──────────────────────────────┐
                     │  grep-backend  (this)        │
                     │                              │
                     │  data/editions.json          │
                     │  data/subscribers.json       │
                     │  uploads/*.pdf               │
                     └──────────────────────────────┘
```

Two things are worth knowing before you change anything:

**The website has two editions built into it.** `grep v0` and `grep v1` are
hand-set TypeScript in `src/lib/editions/`. They render whether or not this
service is running. Anything published here is *merged over* them, so if this
service is down the site still works — it just shows two editions instead of
five. You cannot delete v0 or v1 from the admin. You *can* override one by
publishing an edition with the same slug.

**Pages that show editions are rendered on demand.** The website is no longer a
folder of static files; it needs a running Node process (`npm run build && npm
run serve`). That is what lets a newly published edition appear without anyone
rebuilding and redeploying the site.

---

## Running it locally

You need Go 1.22 or newer (`go version`).

```bash
cd grep-backend
cp .env.example .env
# fill in GOOGLE_CLIENT_ID and ADMIN_EMAILS - see the next two sections
go run .
```

It refuses to start if `GOOGLE_CLIENT_ID` or `ADMIN_EMAILS` is missing. That is
deliberate: starting without them would serve an admin nobody can reach, which
looks like a broken deployment rather than a missing variable.

Check it:

```bash
curl localhost:8080/healthz
# {"status":"ok"}
```

Then the website, in another terminal:

```bash
cd grep-website
cp .env.example .env       # fill in the same GOOGLE_CLIENT_ID
npm install
npm run dev
```

Open <http://localhost:4321/admin>.

Run the tests with `go test ./...`.

---

## Creating the Google sign-in key

This is the part people get stuck on, so it is written out in full. You need to
do it once. It is free.

**What you are creating:** an *OAuth 2.0 Client ID*. It identifies the admin
page to Google. It is not a secret — it ends up in the website's HTML, which is
fine and expected. The thing that actually controls access is
[`ADMIN_EMAILS`](#who-is-allowed-in).

### 1. Make a project

1. Go to <https://console.cloud.google.com/>.
2. Sign in with an ACM-VIT account, not a personal one — whoever holds this
   project controls sign-in for the admin, so it should outlive one student.
3. Click the project dropdown at the top → **New Project**.
4. Name it `grep-admin`. Leave the organisation as it is. **Create**.
5. Wait for it to finish, then make sure it is the selected project in the
   dropdown. Everything below applies to the *selected* project — this is the
   single most common way to configure the wrong thing.

### 2. Configure the consent screen

Google will not issue a client ID until this exists.

1. Left menu → **APIs & Services** → **OAuth consent screen**.
2. User type:
   - **Internal** if ACM-VIT has Google Workspace. Only accounts in the
     organisation can sign in. Pick this if it is available.
   - **External** otherwise. This is fine — the `ADMIN_EMAILS` allowlist still
     does the real work. You do *not* need to submit for verification, because
     the app only requests basic profile scopes.
3. **Create**, then fill in:
   - App name: `grep admin`
   - User support email: your ACM-VIT address
   - Developer contact email: the same
4. **Save and Continue** through Scopes — add nothing. The default `email`,
   `profile` and `openid` are all this needs.
5. On **Test users** (External only): add every address that will sign in. While
   the app is in "Testing" mode only these can get through, *in addition to*
   `ADMIN_EMAILS`. Forgetting this is the usual cause of "Google says access
   blocked" — see [When something is wrong](#when-something-is-wrong).
6. **Save and Continue** → **Back to Dashboard**.

### 3. Create the client ID

1. **APIs & Services** → **Credentials** → **+ Create Credentials** → **OAuth
   client ID**.
2. Application type: **Web application**.
3. Name: `grep admin web`.
4. **Authorised JavaScript origins** — this is the important field. Add the
   origin of the *website*, not this service:

   | Where | Value |
   | --- | --- |
   | Local development | `http://localhost:4321` |
   | Production | `https://grep.acmvit.in` |

   Origins only — scheme, host, optional port. No path, **no trailing slash**.
   `https://grep.acmvit.in/admin` is wrong and will be rejected.
5. **Authorised redirect URIs**: leave empty. Google Identity Services returns
   the token to the page directly; there is no redirect in this flow.
6. **Create**. Copy the **Client ID** — it looks like
   `1234567890-abcdefg.apps.googleusercontent.com`.

### 4. Put it in both places

The same value goes in two files. If they differ, sign-in fails with a token
audience error, because this service checks that the token was minted for the
client it knows about.

```bash
# grep-backend/.env
GOOGLE_CLIENT_ID=1234567890-abc.apps.googleusercontent.com

# grep-website/.env
PUBLIC_GOOGLE_CLIENT_ID=1234567890-abc.apps.googleusercontent.com
```

Restart both after editing. The website bakes `PUBLIC_` values in at build time,
so in production a changed client ID needs a rebuild, not just a restart.

### 5. Rotating it

If the client ID ever needs replacing (it leaked in a way you dislike, or the
project is being handed to a new committee):

1. Create a second OAuth client ID alongside the first.
2. Update both `.env` files and redeploy.
3. Confirm you can sign in.
4. Delete the old client in the console.

Doing it in that order means there is no window where nobody can get in.

---

## Who is allowed in

`ADMIN_EMAILS` in `grep-backend/.env`:

```bash
ADMIN_EMAILS=chair@acmvit.in,tech@acmvit.in,design@acmvit.in
```

Comma-separated, case-insensitive, no spaces needed.

**This is the entire access-control model.** A valid Google account that is not
on this list gets a 401 from every admin route. Adding an address grants the
power to publish to the live site and to delete published editions.

Two properties worth knowing:

- The address must be **verified** on the Google account. An unverified address
  is one the holder has not proved they control, so it is not something to match
  an allowlist against.
- Changing the list takes effect on **restart**. There is no live reload — a
  file that is read once cannot be edited by an attacker who gets partial
  access.

**When someone leaves the committee, remove their address and restart.** That is
the whole offboarding process. Their Google account still exists; it just stops
being an admin.

---

## Publishing an edition

Go to `/admin` on the website. Nothing links there — it is deliberately
unlisted, and the page is `noindex`. Sign in with an allowlisted account.

You get two paths.

### Just the PDF (the quick one)

For when the edition is laid out in print and nobody wants to transcribe it.

| Field | What to put |
| --- | --- |
| Slug | `grep-v2` — becomes `/read/grep-v2`. Lowercase, hyphens. **Permanent.** |
| Number | `2`. Sorts the archive; highest is newest. |
| Name | Optional. `Origins Edition`, or leave blank. |
| Dateline | `March 2027`, as printed on the cover. |
| Publication date | Used by the RSS feed. |
| Pages | Printed page count, or 0. |
| Tagline | One line under the wordmark on the cover. |
| Blurb | Two or three sentences, shown on the archive card. |
| PDF | Upload the file, or paste a bucket link. |
| Cover | `keyboard` or `brick` — which printed pattern the cover uses. |
| Status | `draft` while you check it, `published` when it should be live. |

The reader for a PDF edition shows the cover and a download. No contents rail,
no reading time — there is nothing to count.

### The full edition

Everything above, plus a **Sections** box holding a JSON array. This is what
gives the web reader something to read: the contents rail, the search index and
the reading time all come from it.

The shape is the website's `Section[]` type. Copy a real one from
`grep-website/src/lib/editions/v1.ts` and edit it — that is far easier than
writing it from scratch, and it is what the block types were designed against.

A minimal example:

```json
[
  {
    "id": "chairperson-note",
    "title": "Chairperson's Note",
    "navTitle": "Chairperson's Note",
    "accent": "blue",
    "blocks": [
      { "type": "lead", "text": "A year of building, in fragments." },
      { "type": "p", "text": "The first paragraph of the note." },
      { "type": "signature", "name": "A. Chairperson", "lines": ["Chairperson, ACM-VIT"] }
    ]
  }
]
```

The box validates as you leave it and tells you what is wrong. Saving is blocked
until it parses.

`id` must be unique within the edition — it is the anchor the contents rail
links to.

### Draft first

Save as a **draft**, check it, then edit and set it to **published**. Drafts are
invisible to the public API, so nothing about them reaches the site.

A published edition appears within about a minute — the website caches the list
for 15 seconds and a CDN may hold it a little longer.

---

## Where the files live

The PDF has to be somewhere a reader's browser can fetch. Four ways, in order of
effort.

### Option A — upload through the admin

Click **Upload** next to the PDF field. The file is written to `UPLOAD_DIR` and
served at `/files/<name>`, and the PDF field fills itself in.

Simplest, and fine for a newsletter. Two caveats:

- **The disk must survive restarts.** Many hosts give containers an ephemeral
  filesystem — a redeploy wipes it and every uploaded PDF 404s. See
  [Deploying](#deploying).
- **This process serves the file.** For a handful of PDFs a year that is
  nothing, but it is your bandwidth.

Each upload gets a random prefix (`a1b2c3d4e5f6a7b8-grep-v2.pdf`) so two
editions uploading `grep.pdf` do not collide and an unpublished draft's PDF
cannot be guessed at.

### Option B — Google Cloud Storage

1. <https://console.cloud.google.com/storage> → **Create bucket**.
2. Name it `grep-editions` (bucket names are globally unique — add a suffix if
   taken).
3. Location: `asia-south1` (Mumbai) is closest to VIT.
4. Storage class: **Standard**.
5. Access control: **Uniform**.
6. **Uncheck "Enforce public access prevention"** — the PDFs are meant to be
   public.
7. Create, then **Permissions** → **Grant access**:
   - Principal: `allUsers`
   - Role: **Storage Object Viewer**
   - Save, and confirm the "public" warning.
8. Upload the PDF through the console.
9. Copy the public URL:
   `https://storage.googleapis.com/grep-editions/grep-v2.pdf`
10. Paste it into the PDF field in the admin.

No keys, no SDK, nothing to configure in this service. You are pasting a link.

### Option C — Cloudflare R2

Free egress, which makes it a good choice if the newsletter ever gets popular.

1. Cloudflare dashboard → **R2** → **Create bucket**, name it `grep-editions`.
2. Upload the PDF.
3. **Settings** → **Public access** → **Connect a custom domain**
   (e.g. `files.acmvit.in`), or enable the `r2.dev` development URL.
   > The `r2.dev` URL is rate-limited and Cloudflare asks you not to use it in
   > production. For a few PDFs a year it is fine; a custom domain is better.
4. Copy the object's public URL and paste it into the admin.

### Option D — Amazon S3

1. S3 console → **Create bucket**, name it `grep-editions`, region
   `ap-south-1`.
2. Under **Block Public Access**, uncheck **Block all public access** and
   confirm.
3. Create, then **Permissions** → **Bucket policy**:

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Sid": "PublicRead",
         "Effect": "Allow",
         "Principal": "*",
         "Action": "s3:GetObject",
         "Resource": "arn:aws:s3:::grep-editions/*"
       }
     ]
   }
   ```

4. Upload, then use
   `https://grep-editions.s3.ap-south-1.amazonaws.com/grep-v2.pdf`.

### Serving uploads from a bucket

If you want **Option A**'s convenience with a bucket's durability: mirror
`UPLOAD_DIR` to a bucket and point the links at it.

```bash
PUBLIC_FILES_BASE_URL=https://storage.googleapis.com/grep-editions
```

Uploads then store `https://storage.googleapis.com/grep-editions/<name>` instead
of a link to this process. Sync on a timer:

```bash
# every 10 minutes, from crontab -e
*/10 * * * * gsutil -m rsync -r /srv/grep/uploads gs://grep-editions
```

There is deliberately **no S3/GCS/R2 client in this service**. Pasting a link
covers every one of them with no credentials to store, rotate or leak, and the
mirror above covers the rest. Three vendor SDKs to keep current would be the
most fragile part of this codebase, for no capability you do not already have.

---

## Deploying

Build a static binary:

```bash
CGO_ENABLED=0 go build -o grep-backend .
```

Run it behind a reverse proxy with TLS. Set `PUBLIC_BASE_URL` to the public
address and `ALLOWED_ORIGINS` to the website's origin.

**The filesystem must persist.** Both `DATA_DIR` and `UPLOAD_DIR` hold the real
state.

| Host | Persistence |
| --- | --- |
| A VM (EC2, Compute Engine, Hetzner) | Fine — a normal disk. |
| Fly.io | Attach a volume, mount it, point both vars at it. |
| Render / Railway | Attach a persistent disk. |
| Cloud Run, Lambda, Vercel | **No.** The filesystem is wiped between requests. Use a VM, or a bucket via `PUBLIC_FILES_BASE_URL` plus a database this service does not have. |

A systemd unit:

```ini
[Unit]
Description=grep-backend
After=network.target

[Service]
Type=simple
User=grep
WorkingDirectory=/srv/grep
EnvironmentFile=/srv/grep/.env
ExecStart=/srv/grep/grep-backend
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now grep-backend
sudo journalctl -u grep-backend -f
```

Then the website, on the same box or another:

```bash
cd grep-website
npm ci && npm run build
npm run serve          # needs GREP_API_URL in the environment
```

---

## Backing it up

Everything that matters is two JSON files and a directory of PDFs.

```bash
#!/usr/bin/env bash
# /srv/grep/backup.sh - keeps 30 days
set -euo pipefail
STAMP=$(date +%F)
tar czf "/srv/backups/grep-$STAMP.tar.gz" -C /srv/grep data uploads
find /srv/backups -name 'grep-*.tar.gz' -mtime +30 -delete
```

```bash
# crontab -e - 03:00 daily
0 3 * * * /srv/grep/backup.sh
```

Restoring is `tar xzf` into place and a restart. Because writes go through a
temp file and an atomic rename, a backup taken mid-write gets the previous
complete file, never a half-written one.

Check a backup once a term. An untested backup is a guess.

---

## Handing over

When the committee changes, in this order:

1. **Add the new people** to `ADMIN_EMAILS`. Restart. Confirm each can sign in.
2. **Transfer the Google Cloud project** — IAM & Admin → add the new lead as
   **Owner**. If it is on a personal account, move it to an ACM-VIT one now.
3. **Transfer the bucket**, if you use one.
4. **Hand over the server**: SSH access, the `.env` file, where backups go.
5. **Remove the leavers** from `ADMIN_EMAILS`. Restart.
6. **Check the backup restores** with the new lead watching.

Step 5 last, so nobody is locked out mid-handover.

---

## The API

Everything under `/v1/admin/` needs `Authorization: Bearer <google-id-token>`
from an allowlisted account. Everything else is public.

| Method | Path | What |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness. |
| `GET` | `/v1/editions` | Published editions. The website reads this. |
| `GET` | `/v1/editions/{slug}` | One published edition. |
| `POST` | `/v1/subscribe` | Record an address. The website's form posts here. |
| `GET` | `/files/{name}` | An uploaded file. |
| `GET` | `/v1/admin/me` | Who the token belongs to. |
| `GET` | `/v1/admin/editions` | All editions, drafts included. |
| `GET` | `/v1/admin/editions/{slug}` | One, draft or not. |
| `POST` | `/v1/admin/editions` | Create. 409 if the slug exists. |
| `PUT` | `/v1/admin/editions/{slug}` | Replace. The path slug wins. |
| `DELETE` | `/v1/admin/editions/{slug}` | Delete. |
| `POST` | `/v1/admin/uploads` | Multipart, field `file`. Returns `{name, url}`. |
| `GET` | `/v1/admin/subscribers` | Captured addresses. |

A bad token and a good token belonging to nobody on the list both return the
same 401 with the same message, so a stranger cannot use the difference to
learn who the admins are.

---

## When something is wrong

**"Access blocked: grep admin has not completed the Google verification
process"**
Your consent screen is External and in Testing, and the address is not a test
user. Add it under **OAuth consent screen → Test users**. Verification is not
needed for the scopes this uses.

**Sign-in button never appears**
`PUBLIC_GOOGLE_CLIENT_ID` is unset in the website's environment. The page says
so where the button should be. Remember `PUBLIC_` values are baked in at build
time — set it and rebuild.

**"The given origin is not allowed for the given client ID"**
The website's origin is missing from **Authorised JavaScript origins**. It must
match exactly: scheme, host, port, and no trailing slash. Changes can take a few
minutes to propagate.

**Signed in, then every action returns 401**
- The client IDs in the two `.env` files differ. They must be identical.
- Or your address is not in `ADMIN_EMAILS` — check spelling and restart after
  editing.
- Or the token expired; Google ID tokens last an hour. Sign in again.

**Browser console: "blocked by CORS policy"**
The website's origin is not in `ALLOWED_ORIGINS`. Add it, no trailing slash,
restart.

**A published edition does not show on the site**
- Status is `draft`, not `published`.
- Or the website cannot reach this service — check `GREP_API_URL` and its logs
  for `[editions] could not reach the admin service`. The site falls back to the
  built-in editions rather than erroring, so this fails quietly by design.
- Or you are within the 15-second cache. Wait.

**Uploaded PDF 404s**
`PUBLIC_BASE_URL` does not match where the service actually is, so the stored
link points somewhere wrong. Fix it and re-save the edition — old editions keep
the old link.

**Service will not start**
Read the first log line. It names the missing variable.
