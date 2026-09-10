# Security Policy

[简体中文](SECURITY.zh-CN.md)

Chora is a source-checkout Local Alpha validated only on Apple Silicon macOS.
It is not a public release and makes no production-security or cross-platform
support claim.

## Reporting a vulnerability

Do not disclose suspected vulnerabilities, credentials, private endpoints, or
reproduction data in a public issue, discussion, patch, or log.

For the public repository, use [GitHub private vulnerability reporting](https://github.com/Yangyang96/chora/security/advisories/new).
Do not open a public issue for a suspected vulnerability. This route becomes
available only after the repository owner enables private vulnerability
reporting; publication remains blocked until that setting has been enabled and
tested.

Include only the minimum material needed to reproduce the issue. Do not send
credentials, private runtime assets, or unrelated personal or company data.

The [Roadmap](ROADMAP.md) distinguishes the current public target and
pending gates. Local Connected uses user-installed/configured Pi, does not
require private M1 images, and makes no Sandbox-isolation claim.

## Current boundary

Security fixes must preserve fail-closed managed execution, exact runtime and
Engine identity checks, sandbox isolation, owner-only credential handling, and
the prohibition on silent direct-host fallback. `Trusted Local · No Sandbox` is
an explicit, separately acknowledged mode and must never be presented as an
isolation boundary.

No response time, embargo window, bounty, disclosure date, or supported-version
policy is promised during this Local Alpha.
