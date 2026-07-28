# Public usage

This document describes how to configure a clean deployment without including
account data, private business rules, or credentials in Git.

## Prepare local configuration

Copy the template and edit only the local copy:

```powershell
Copy-Item .env.example .env
notepad .env
```

At minimum, set your own group IDs and Agent endpoint:

```dotenv
QQ_PLATFORM=native
QQ_GROUP_ALLOWLIST=123456789
QQ_PRIVATE_ALLOWLIST=987654321
AGENT_API_MODE=openai
AGENT_API_URL=https://api.example.com/v1/chat/completions
AGENT_MODEL=your-model
```

Keep `AGENT_API_KEY`, `SESSION_ENCRYPTION_KEY`, `ADMIN_API_TOKEN`,
OneBot tokens, cookies, and provider credentials in process or user environment
variables. Do not put their values in `.env`, JSON, Markdown, logs, or screenshots.

## Configure an isolated binding

Create or edit the local `data/chat-bindings.json`. The complete `data/` directory
is ignored by Git, so it can contain deployment-specific Personas, Knowledge,
Skills, MCP references, session databases, and generated media.

```json
{
  "bindings": [
    {
      "name": "support-group",
      "platform": "*",
      "self_id": "1000000000",
      "chat_type": "group",
      "chat_ids": ["123456789"],
      "persona": "support",
      "tools": ["capture_webpage"],
      "knowledge_bases": ["support"],
      "require_mention": true,
      "learning_enabled": true
    }
  ]
}
```

Only the resources named by a binding are visible to that chat. Group history,
private-chat history, summaries, and learning memory remain separate even when
the same Persona is reused.

## Add local business behavior

Put private Persona prompts under `data/personas.json`, private knowledge under
`data/knowledge/`, and private files under `files/`. Do not add those files to a
public commit. The tracked `examples/` files contain placeholders only.

For webpage screenshots, use an exact HTTPS host allowlist:

```dotenv
WEB_SCREENSHOT_ENABLED=true
WEB_SCREENSHOT_BROWSER_PATH=C:\Program Files\Google\Chrome\Application\chrome.exe
WEB_SCREENSHOT_ALLOWED_HOSTS=example.com
```

The browser does not reuse the desktop login session. Do not add internal
domains or authenticated URLs to a public example.

## Start and verify

```powershell
.\scripts\build.ps1
cmd.exe /k powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\start.ps1
```

After login, verify the local runtime:

```powershell
Invoke-RestMethod http://127.0.0.1:18080/healthz
Invoke-RestMethod http://127.0.0.1:18080/readyz
```

Stop the foreground process with `Ctrl+C`.

## Check before publishing

Before committing or pushing, run:

```powershell
.\scripts\check-public-release.ps1
```

The check scans tracked files and staged additions for private paths, credential
patterns, local QQ paths, and optional patterns from the ignored
`.release-private-patterns.txt`. Add one private term or URL per line to that
local file when a deployment has business-specific identifiers. The file itself
is ignored and must never be staged.

## Build public packages

Run the packaging command only from the sanitized public branch:

```powershell
.\scripts\package-release.ps1 -Version 0.2.0
```

The script creates Windows x64, macOS Intel, and macOS Apple Silicon archives
under `dist/`, plus a SHA-256 checksum file. The Windows archive contains the
native QQNT loader and hook. The macOS archives contain only the Go runtime and
support `QQ_PLATFORM=onebot`; native QQNT injection is Windows-only.

The packaging process clears `GOFLAGS` for release builds, so ignored local
build-tag extensions cannot enter public archives. Do not upload `dev`, `.env`,
`data/`, `files/`, `exports/`, local extension files, or the release staging
directory.
