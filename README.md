# Chora

> Project contains multiple repository resources and topic Rooms; each Task
> belongs to one Room and selects one or more Project repositories. Create a
> Project, attach existing local Git repositories, then open its default General
> Room or create a named topic Room. Rooms share Project resources but keep their
> own tasks, brief and history. See the [roadmap](ROADMAP.md) for first-release
> versus later scope.

[简体中文](README.zh-CN.md) · [Documentation](docs/README.md) ·
[Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) · [Roadmap](ROADMAP.md)

Chora is a local-first workspace where people and coding agents collaborate in
the same project Room. A user describes a change; Chora turns it into a bounded
Spec Coding task, shows the plan, execution and truthfully attributed checks,
and leaves the final patch decision with the user.

> **Project status:** pre-publication source-checkout Developer Alpha on Apple
> Silicon macOS. The local Pi Workbench golden path has passed technical validation;
> the S3 daily-use/public usability implementation is present, and integrated
> qualification plus overall user acceptance are pending. No frozen
> public candidate has passed release acceptance. See the [Roadmap](ROADMAP.md).

## Daily-use Workbench storage

`chora workbench --source /absolute/path/to/chora` defaults to `$HOME/.chora/data`.
Task worktrees are centrally managed at
`$HOME/.chora/data/task-workspaces/<task-hash>/<repo-hash>/`, outside source repositories.
Both hashes are 12 hexadecimal characters; full identities remain frozen in Task records.
Existing Tasks keep their recorded paths, and occupied roots are checked for exact ownership.
Use an explicit absolute `--data` for isolated tests or another persistent data root.
Existing instances keep their configured directory; this default does not migrate data.

New Task branches use short names such as `feature/<12-hex-hash>`, `fix/<12-hex-hash>`,
or `chore/<12-hex-hash>`. Explicit task-title types such as `fix:` take precedence;
common Chinese prefixes are also recognized, with `feature` as the fallback.
The hash binds the full Task and repository identities. Creation checks ownership
and never overwrites a colliding branch. Existing Task branches and their review
and delivery records remain unchanged; branch names are independent of worktree paths.

## Why Chora?

Coding agents are useful, but their work often disappears into terminal output
or an opaque external session. Chora keeps the collaboration itself as durable
product state:

~~~text
requirement
→ Room context and acceptance criteria
→ visible technical plan
→ selected repositories and target branches, with Task-owned Git branches/worktrees
→ visible Agent attempt
→ checks with explicit provenance
→ reviewable patch
→ review and accept, ask the Agent to fix, or change the requirement
→ retain reviewed, uncommitted changes in each Task worktree
~~~

Chora records the Task, attempts, decisions, checks, patch, and review history
in SQLite so the Room can be reopened after a restart. New multi-repository Tasks
use independent branches; accepting a patch saves its review and leaves the edits
uncommitted in the Task worktree. The result shows the target branch, Task branch
and worktree path. Review does not write back to the original checkout. Legacy
detached Tasks retain their existing Apply flow.

After acceptance, each repository offers Commit → Push → GitHub PR → Merge →
worktree cleanup as separate preview-and-confirm actions. GitHub.com same-repository
PRs use your installed, authenticated `gh`; merge commits respect provider policy.
Commit/Push do not require gh. Refresh reconciles uncertain outcomes without replay.
Cleanup requires a proven merged PR and a clean Task worktree, including ignored
files, and retains branches and review history. Feedback requiring changes uses a
new Task and Review with an explicitly selected branch; immutable prior reviews do
not authorize changed PR contents.

## What works today

- A Room-based UI for creating and reopening Spec Coding tasks.
- Projects with multiple local Git repository resources and multiple topic Rooms;
  each Task explicitly selects its repository resources.
- Automatic planning with visible progress, checks, blockers, and decisions.
- Task-owned Git branches/worktrees, with historical detached worktree compatibility.
- Repository target defaults, per-Task target selection, and automatic, named,
  or explicit no-check policies without a required manual file list.
- Human Review with uncommitted edits retained in each Task worktree; original checkout preserved.
- A deterministic Fake Agent for development and browser tests.
- A bounded click-to-install Pi 0.85.1 path plus compatible PATH Pi reuse for
  Local Connected execution in Task worktrees, without Docker.
- A retained M1 Pi/Docker path with independent verification and private inputs.
- Explicit check provenance: Local Connected Pi checks are Agent-reported.
- Cancel, recovery, retry, archive/restore, and process-restart persistence.
- Explicit closure of eligible remaining Result delivery without deleting files,
  separate from guarded repository worktree cleanup.
- An explicit `Trusted Local · No Sandbox` profile with a disclosure gate.

## Try the local demo

The quickest way to explore the product uses deterministic fixtures. It does
not call a cloud model and does not prove the real Runtime or Sandbox path.

### Prerequisites

- Node.js 22.12 or newer
- npm 11 or newer
- Go 1.26 or newer

~~~sh
npm ci
npm run web:build
CHORA_DEMO_ROOT="$(mktemp -d /private/tmp/chora-demo.XXXXXX)"
go run ./tools/e2eserver \
  --db "$CHORA_DEMO_ROOT/chora.db" \
  --web "$PWD/web/dist" \
  --port 8787
~~~

Open <http://127.0.0.1:8787>. Press `Ctrl-C` to stop the server. The temporary
directory is not removed automatically.

## Local Pi Workbench and retained M1 path

Workbench can reuse a compatible user-installed `pi` on PATH or, after an
explicit click, install the frozen Pi 0.85.1 package into the Chora data root.
It never installs Node or silently replaces an existing Pi. Pi requires Node
22.19+; the Chora demo's lower Node minimum does not cover Pi. The admitted
version floor for an existing Pi is 0.84.2; 0.85.1 is the exact managed and
real golden-path version. A lower bound is not a qualification of every later
version. Pi owns credentials and model configuration: launch the selected Pi,
use `/login` and `/model`, then return to Workbench and refresh readiness. Each
Attempt shows the provider/model Pi actually reported; an unobserved identity
remains unknown and changing Pi defaults does not rewrite prior provenance.

The development entry is `go run ./cmd/chora workbench`, with absolute `--source`
and `--data` paths (data outside source), a built `web/dist`, and optional
`--port` (8787 by default). Choose an existing local Git folder in the macOS
chooser, then explicitly acknowledge Local Connected / No Sandbox. Clone and
manual-path creation UI are retired. The onboarding and maintenance surfaces are
implemented; exact frozen-candidate reproduction and integrated S3/O5 acceptance
remain pending and are not claimed complete here.

This route does not require private M1 Docker images or maintainer OAuth. A
Project may attach multiple existing Git repositories. Each Task selects its
repository resources and freezes each selected target branch's commit/tree and
check policy. Choose automatic discovery, persisted named checks, or explicit
**no checks** per repository; no manual file list is required. Automatic discovery
uses committed project configuration, while unrecognized or unobserved checks
remain Unverified. Settings changes affect new Tasks only.
Ordinary UTF-8 text additions, edits, deletions and renames (shown as delete/add)
are reviewed together and retained uncommitted in the Task worktree. Commit and
subsequent delivery require separate confirmation; original-checkout Apply remains for legacy Tasks.
For current Task branches, each repository uses Review → Commit → Push → GitHub
PR → Merge → Cleanup as explicit preview-and-confirm steps. A mixed Result can
close only its eligible remaining delivery after uncertain writes are reconciled;
closure preserves delivered facts and files and is separate from cleanup.
Large repositories are admitted without the former whole-repository 4,096-file
or 128 MiB gate; discovery and reads are demand-driven and bounded. Unchanged
binary, LFS, submodule and large-file content may remain in a repository, while
changes to binary/LFS/submodule content and executable modes are rejected
explicitly at Result validation. Ordinary text reads retain an 8 MiB per-file
limit. Ignored output is excluded; unrelated files and dirty checkout content
are preserved.
Successful no-change Tasks retain their report without an Apply action. Missing or
truncated results remain errors. Setup commands must be explicitly declared;
dependencies are never copied or installed automatically. Pi's attempt timeout
is displayed; unavailable cost stays unknown.
These implementation capabilities do not imply public Alpha/candidate acceptance.

The retained M1 isolated path below still requires private pre-provisioned
images/authentication and is not a public installation route. Do not weaken its
isolation checks. Its [developer validation record](docs/local-alpha-development-validation.md)
is historical maintainer guidance, not the current Workbench startup guide.

<details>
<summary>Maintainer-only exact Source-checkout reference</summary>

The retained path requires Apple Silicon macOS, Node.js 22.12+, npm 11+, Go
1.26+, Pi 0.84.2, Docker Engine/CLI 29.6.1 through Colima 0.10.3, an owner-only
Pi OAuth file, and the two already provisioned images below.

```sh
npm ci
npm run web:build
CHORA_SOURCE_ROOT="$(pwd -P)"
CHORA_SOURCE_REVISION="$(node -p "require('./contracts/g2-m4/source-baseline-v6/manifest.json').source_revision")"
CHORA_TARGET_ROOT="$(dirname "$CHORA_SOURCE_ROOT")/chora-source-alpha-target"
CHORA_DATA_ROOT="$(dirname "$CHORA_SOURCE_ROOT")/chora-source-alpha-data"
CHORA_PI_IMAGE=sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7
CHORA_BOUNDARY_IMAGE=sha256:4f7746f3cdbe55dc454775ead5958a9ed8a78b93776ea1df598255c1606b25c6
export CHORA_PI_CODEX_AUTH_FILE=/absolute/path/to/owner-only-auth.json
chmod 600 "$CHORA_PI_CODEX_AUTH_FILE"
git worktree add --detach "$CHORA_TARGET_ROOT" "$CHORA_SOURCE_REVISION"
install -d -m 700 "$CHORA_DATA_ROOT"
DOCKER_CLI="$(realpath "$(command -v docker)")"

test "$(git -C "$CHORA_TARGET_ROOT" rev-parse HEAD)" = "$CHORA_SOURCE_REVISION"
test -z "$(git -C "$CHORA_TARGET_ROOT" status --porcelain)"
colima ssh -- test -d "$CHORA_SOURCE_ROOT"
colima ssh -- test -d "$CHORA_TARGET_ROOT"
colima ssh -- test -d "$CHORA_DATA_ROOT"
test "$("$DOCKER_CLI" --context colima image inspect "$CHORA_PI_IMAGE" --format '{{.Id}}')" = "$CHORA_PI_IMAGE"
test "$("$DOCKER_CLI" --context colima image inspect "$CHORA_BOUNDARY_IMAGE" --format '{{.Id}}')" = "$CHORA_BOUNDARY_IMAGE"

go run ./cmd/chora source-checkout \
  --source "$CHORA_SOURCE_ROOT" \
  --repository "$CHORA_TARGET_ROOT" \
  --data "$CHORA_DATA_ROOT" \
  --auth "$CHORA_PI_CODEX_AUTH_FILE" \
  --docker-cli "$DOCKER_CLI" \
  --docker-context colima \
  --port 8787
```

Startup verifies the clean pinned checkout, VM-visible paths, exact image IDs,
and owner-only credentials. Missing prerequisites fail closed.

</details>

## Execution profiles

**Standard** is the retained managed/isolated profile. It runs the managed Pi Runtime
inside the qualified Docker Sandbox with a pinned capability policy. The
current launch uses `--no-skills`. Isolation uncertainty fails closed.

**Trusted Local · No Sandbox** is a separate opt-in profile. It runs Pi directly
on the host and may inherit local configuration, tools, credentials,
filesystem access, and network access. The UI requires acknowledgement of the
exact current disclosure policy before it can be selected. It is never an
automatic fallback for Standard.

The repository also contains an implemented installed-product lifecycle
(Doctor, Setup, Upgrade, bounded GC, and Uninstall). Packaging, authenticated
asset distribution, and installed-product acceptance are not complete, so this
is reference implementation work rather than a supported installation route.

## Repository map

| Path | Purpose |
| --- | --- |
| `cmd/chora/` | Chora CLI and product composition |
| `internal/domain/` | Rooms, Tasks, Runs, decisions, reviews, and invariants |
| `internal/app/` | Product use cases and ports |
| `internal/localweb/` | Local HTTP API, UI serving, and runtime composition |
| `internal/agent/` | Fake and Pi Agent adapters |
| `internal/dockersupervisor/` | Managed Docker Sandbox execution |
| `internal/trustedhost/` | Explicit unsandboxed local execution |
| `internal/verifier/` | Independent verification |
| `internal/store/sqlite/` | Durable SQLite persistence |
| `web/` | React and TypeScript UI |
| `contracts/`, `distribution/` | Pinned policies and release inputs |
| `migrations/` | SQLite schema migrations |
| `e2e/` | Playwright and Node.js end-to-end checks |
| `tools/` | Development, validation, and fixture tools |
| `spikes/` | Historical experiments; not part of the supported product path |

## Development

Install dependencies before running repository checks:

~~~sh
npm ci
go mod download all
make public-test
make test
make verify
make vet
npm run e2e
git diff --check
~~~

`make verify` includes Go race tests plus Web type-check, test, and build
steps. Playwright may require a local browser installation:
`npx playwright install chromium`.

`npm run e2e:public` (or `make public-e2e`) runs the maintained deterministic
public journey and Project-entry browser coverage. It is credential-free and is
the public CI entry; it does not prove real Pi behavior. Use
`npm run test:e2e:pi-local-connected` explicitly for the configured real-Pi
journey. A skipped real-Pi case is not acceptance evidence.

`make public-test` is the current reproducible public-source gate. The complete
maintainer gates also use frozen Baseline and Source-bundle inputs that are not
part of the first public candidate.

Read [CONTRIBUTING.md](CONTRIBUTING.md) before proposing changes. The
[documentation index](docs/README.md) separates current product guidance from
historical decisions and research.

## Current limitations

- Only one Apple Silicon macOS development environment is validated.
- The S3 Local Connected onboarding, maintenance and public deterministic checks
  are implemented, but exact-candidate integrated qualification and user
  acceptance are pending; see the Roadmap for the remaining gates.
- Private M1 OAuth/images are not publicly distributed and are not a Local
  Connected Workbench prerequisite.
- Public packaging, upgrades, release recovery, and distribution are not ready.
- Multi-user authorization, remote worker fleets, multi-repository atomic
  changes, automatic SCM publishing, and production delivery are future work.
- Unreviewed experiments, raw evidence, vendored dependencies, snapshots, and
  private Runtime/authentication inputs are excluded from the first public
  candidate; see the [public source scope](docs/publication-scope.md).

## Community, security, and license

- Contribution guide: [CONTRIBUTING.md](CONTRIBUTING.md)
- Code of Conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
- Security policy: [SECURITY.md](SECURITY.md)
- Support status: [SUPPORT.md](SUPPORT.md)
- Maintenance and redacted diagnostics: [docs/workbench-maintenance.md](docs/workbench-maintenance.md)
- Public source scope: [docs/publication-scope.md](docs/publication-scope.md)

Chora is licensed under the [GNU Affero General Public License v3.0](LICENSE).
