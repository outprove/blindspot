# What is your Blindspot?

A Go web app that preserves the current product UX while running on PocketBase as the embedded backend/runtime.

## Current features

- public landing page for unregistered visitors
- registration and login with email-based user IDs
- generated unique public profile names
- one active question at a time, plus completed-question history
- anonymous public responses at `/user/{profile_name}`
- response history grouped by the exact question that received it
- SMTP email delivery for submitted answers when configured

## Run locally

If you already have Go installed:

```powershell
go build -mod=mod .
.\main.exe serve --http=127.0.0.1:8091
```

If you want to use the bundled SDK in this workspace:

```powershell
$env:GOROOT = (Resolve-Path .\tools\go-sdk)
$env:GOCACHE = (Resolve-Path .\.gocache)
$env:GOMODCACHE = (Resolve-Path .\.gomodcache)
.\tools\go-sdk\bin\go.exe build -mod=mod .
.\main.exe serve --http=127.0.0.1:8091
```

Open [http://127.0.0.1:8091](http://127.0.0.1:8091) after starting the server.

PocketBase also exposes:

- app routes at `/`
- health check at `/health`
- API base at `/api/`
- admin dashboard at `/_/`

## Important files

- `main.go` contains the Go server, auth flow, and database logic
- `config.go` and `config.json` contain runtime configuration such as session secrets and SMTP settings
- `src/sample_python_project/templates/` contains the server-rendered HTML templates
- `src/sample_python_project/static/styles.css` contains shared styling

## Configuration

Edit `config.json` to control local runtime settings.

Key fields:

- `session_secret` signs login sessions
- `email_provider` chooses `smtp` or `resend`
- `smtp.host`
- `smtp.port`
- `smtp.username`
- `smtp.password`
- `smtp.from_email`
- `resend.api_key`
- `resend.from_email`

`config.example.json` shows a filled-in example. If the SMTP host or from-email is left blank, the app keeps the current fallback behavior and skips email delivery.

## Deploy to Railway

This app can run on Railway as a single Docker-based service.

Important deployment note:

- keep this app as a single instance
- attach a persistent volume at `/app/pb_data`
- use Railway environment variables for secrets instead of committing `config.json`

### Files already prepared for Railway

- `Dockerfile`
- `railway-start.sh`
- `.dockerignore`

### Railway setup steps

1. Push this repo to GitHub.
2. In Railway, create a new project from that GitHub repo.
3. Let Railway detect and build the app from the included `Dockerfile`.
4. Add a volume and mount it at `/app/pb_data`.
5. Set these environment variables in Railway:

```text
SESSION_SECRET=use-a-long-random-secret
ADMIN_PASSWORD=choose-a-real-admin-password
EMAIL_PROVIDER=smtp
SMTP_HOST=
SMTP_PORT=587
SMTP_USERNAME=
SMTP_PASSWORD=
SMTP_FROM_EMAIL=
RESEND_API_KEY=
RESEND_FROM_EMAIL=
```

6. Deploy.
7. Verify the app health check at `/health`.

### Notes

- `config.json` is intentionally excluded from the Docker build.
- The app will start even without `config.json`; Railway env vars now override file-based config.
- If `EMAIL_PROVIDER=resend`, the app uses the Resend Email API instead of SMTP.
- If the selected provider is not configured, email delivery keeps the current fallback behavior.
