# Support

[简体中文](SUPPORT.zh-CN.md)

Chora has no public release or general support service. The current
source-checkout Local Alpha is validated only on Apple Silicon macOS with the
exact toolchain and runtime boundary documented in the repository README.

There is no promised response time, service level, compatibility window, public
download, or hosted service. Use [GitHub Issues](https://github.com/Yangyang96/chora/issues)
for reproducible bugs, focused feature requests, and documentation problems.
Do not send credentials, private assets, or vulnerability details with a
support request; use the private route in the [Security Policy](SECURITY.md).

The [Roadmap](ROADMAP.md) distinguishes implemented S3 behavior from pending
integrated qualification and publication gates. Local Connected can use the
frozen Workbench-managed Pi 0.85.1 or a compatible user-installed PATH Pi. It
does not require private M1 images and makes no Sandbox-isolation claim. Pi owns
authentication and model selection through `/login` and `/model`; do not include
Pi credentials or configuration output in a report.

## Before requesting help

Confirm that you are using the documented versions and commands. For Workbench
data or prerequisite failures, run the read-only redacted doctor documented in
the [maintenance guide](docs/workbench-maintenance.md). Run the smallest other
relevant check and capture:

- the command and concise, redacted error output;
- operating system and CPU architecture;
- the redacted `chora workbench doctor` status/finding code, or only the relevant
  Node.js, npm, Go, Pi, Docker, and Colima versions;
- whether the disposable Fake-Agent demo or real-Agent source path failed;
- the expected result and the first observed blocker.

Do not attach OAuth files, environment dumps, absolute private paths, Docker
context endpoints, image archives, database files, or generated acceptance
evidence. A failure on Intel macOS, Linux, Windows, or a different Engine is
useful investigation data, not evidence of a supported configuration.

For deterministic public-journey failures, name whether `npm run e2e:public`
or `make public-e2e` failed. Real Pi qualification uses the separate
`npm run test:e2e:pi-local-connected` entry; a skipped real case is not proof.

Issues are a best-effort community intake, not a support contract. Search
existing issues first and keep each report focused on one reproducible problem.
