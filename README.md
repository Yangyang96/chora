# Chora

**An open-source workspace for people and AI agents to build software together.**

**English** | [简体中文](README.zh-CN.md)

[Quick start](#quick-start) · [Your first task](#your-first-task) · [FAQ](#faq) · [Project status](#project-status) · [Documentation](docs/README.md)

Describe a change, watch the agent work, inspect the diff and checks, then decide
what to commit and publish. Chora keeps your repositories, conversations, tasks,
and review history together in a browser-based workspace on your machine.

Chora's destination is a self-hostable, model-neutral development workspace for
people and agents to plan, build, review, and deliver software across projects
and repositories. The current release scope is a **source-based Developer Alpha**
for Apple Silicon macOS with Pi. See [project status](#project-status) for what
is implemented and what is still planned.

## What you can do

- **Work across repositories.** Group local Git repositories in a project and
  choose which ones each task uses.
- **Follow the work.** See the plan, progress, checks, and questions as the agent
  works. Cancel or retry when needed.
- **Review before publishing.** Inspect changes on a separate task branch and
  worktree, then confirm Commit, Push, PR, and Merge individually.
- **Pick up where you left off.** Reopen projects, topic conversations, and task
  history after restarting Chora.

```text
Describe a change → Agent works → Review diff and checks → Commit → Push → PR
```

## Quick start

### 1. Get the prerequisites

Use an **Apple Silicon Mac** with these tools available in your terminal:

| Tool | Required version / purpose |
| --- | --- |
| Git | Clone Chora and work with local repositories |
| Node.js | 22.19 or newer, required for Pi |
| npm | 11 or newer |
| Go | 1.26 or newer |
| Model access | Your own provider credentials or subscription configured in Pi |

Docker is not required for this setup. GitHub CLI (`gh`) is optional until you
want to create or merge GitHub PRs from Chora.

### 2. Build and start Chora

```sh
git clone https://github.com/Yangyang96/chora.git
cd chora
npm ci
npm run web:build
go run ./cmd/chora workbench --source "$(pwd -P)"
```

When the terminal prints `Chora Workbench listening on http://127.0.0.1:8787`,
open [Chora in your browser](http://127.0.0.1:8787). Keep the terminal running.
Use the language switch in the app to choose English or Chinese.

To stop Chora, press `Ctrl-C`. To start it again, run the last command from the
same directory. Your data is kept in `~/.chora/data`.

### 3. Connect Pi to your model

Pi is the coding agent that executes tasks for Chora.

1. Open the Pi installation panel. Chora can reuse a compatible `pi` on your
   `PATH`; otherwise, click **Install fixed Pi version** to install Pi 0.85.1.
2. Run the configuration command shown in the panel in another terminal. For
   an existing PATH installation, this is `pi`.
3. In Pi, use `/login` to configure your provider and `/model` to select a model.
4. Return to Chora and click **Refresh**. If the panel asks you to restart Chora,
   stop it with `Ctrl-C` and run the startup command again.

This workflow uses **Trusted Local · No Sandbox** (Local Connected). Read and
acknowledge the in-app disclosure before starting: Pi runs on your host with
access to local tools, files, credentials, and the network. Task worktrees
separate code changes; they are not a security sandbox. Model requests go to
your configured provider.

## Your first task

Start with a small change in a repository you trust, such as fixing a validation
message or adding a focused test.

1. **Create a project.** Use the folder chooser to select an existing local Git
   repository with at least one commit. You can add more repositories later.
2. **Open its General room.** A room groups conversations and tasks around a
   topic; you can create other rooms as your project grows.
3. **Describe the change.** Select the repository and target branch. Leave checks
   on automatic discovery, or choose named checks or explicitly no checks.
   You do not need to list every file the agent may edit.
4. **Start and follow the task.** Chora creates a task branch and worktree. Watch
   the plan and progress, and respond if a decision is needed.
5. **Review the result.** Read the diff and check results. Accept it or ask for a
   fix. Acceptance retains reviewed changes in the task worktree; it does not
   commit, push, or write them back into your original checkout.
6. **Deliver when ready.** For each repository, preview and confirm **Commit →
   Push → GitHub PR → Merge**. PR actions require authenticated `gh`; Commit and
   Push use your Git configuration. Cleanup is offered after a confirmed merge
   when the task worktree is clean.

Checks reported by Pi are labeled **Agent-reported**. Missing or unobserved checks
remain **Unverified**. Review the actual results before deciding to publish.

## Try the interface without a model

After the clone, install, and build steps above, run this instead of Workbench:

```sh
CHORA_DEMO_ROOT="$(mktemp -d /private/tmp/chora-demo.XXXXXX)"
go run ./tools/e2eserver \
  --db "$CHORA_DEMO_ROOT/chora.db" \
  --web "$PWD/web/dist" \
  --port 8787
```

Open [the local demo](http://127.0.0.1:8787). It uses scripted examples and makes
no model calls; it does not perform real AI coding. Stop any other Chora server
on port 8787 first. Press `Ctrl-C` to stop; the temporary data is not removed
automatically.

## FAQ

### Does Chora change my original checkout?

New tasks work in separate Git worktrees under `~/.chora/data/task-workspaces/`.
Accepting a review leaves those changes on the task branch. Publishing is a
separate action. Existing uncommitted changes in your checkout are not copied
into a task's committed base.

### Where are my tasks stored?

Projects, tasks, and history live in `~/.chora/data`, together with managed task
worktrees. Restart with the same data directory to reopen them. You can choose
another absolute path with `--data /absolute/path/to/data`, outside the Chora
source directory. See [backup and recovery](docs/workbench-maintenance.md)
before moving, deleting, or upgrading data.

### Pi is not ready, or the browser will not open. What should I check?

Check Node's version with `node --version`, finish Pi login and model selection,
then refresh the installation panel. Follow any restart prompt. If port 8787
is occupied, add `--port 8788` to the startup command and open that port instead.
For other failures, see [troubleshooting and diagnostics](docs/workbench-maintenance.md).

### Can I use Linux, Windows, another agent, or a sandbox?

The current real-agent setup is Pi on Apple Silicon macOS. Cross-platform CI
checks do not establish support for the complete workflow on other systems.
The retained isolated Docker path requires private maintainer inputs and is
not a public setup option. More providers and deployment choices are future
work; see the [roadmap](ROADMAP.md).

### What changes are supported?

Tasks can add, edit, delete, and rename ordinary UTF-8 text files across selected
repositories. Changes to binary files, Git LFS content, submodules, and executable
modes are currently rejected. Dependencies are not installed automatically;
declare any setup commands your task needs. Multi-repository delivery proceeds
one repository at a time, without an atomic cross-repository merge.

## Project status

**As of September 10, 2026:** source code is public; Chora is in Developer Alpha.
Source availability does not mean a stable release or completed user acceptance.

| Area | Progress |
| --- | --- |
| Personal workspace | Implemented: multi-repository projects, topic rooms, task branches/worktrees, visible execution, review, retry, and restart recovery. |
| Code delivery | Implemented: per-repository Commit → Push → GitHub PR → Merge → cleanup, with separate confirmations. |
| Getting started and maintenance | Implemented: Pi installation/configuration guidance, model provenance, backup/restore, and local diagnostics. |
| Current supported setup | Source checkout on Apple Silicon macOS, local Git repositories, and Pi in explicit Local Connected / No Sandbox mode. |
| Release readiness | Public source and local validation are available. Final user acceptance and a tagged release remain pending; there is no packaged desktop installer. |
| Planned expansion | Public sandbox setup, richer model controls, team and agent collaboration, background/remote work, and broader provider support. |

The full workspace described above is the product direction. Team permissions,
multi-agent delegation, and remote execution are future work. The
[roadmap](ROADMAP.md) explains the longer-term stages and their historical context.

## Documentation and contributing

- [Documentation](docs/README.md) — guides and technical references.
- [Roadmap](ROADMAP.md) — current scope and future work.
- [Contributing](CONTRIBUTING.md) — development setup and required checks.
- [Support](SUPPORT.md) — help and bug reports.
- [Security](SECURITY.md) — report vulnerabilities privately.
- [Code of conduct](CODE_OF_CONDUCT.md) — community expectations.

Chora is licensed under the [GNU Affero General Public License v3.0](LICENSE).

---

**English** | [简体中文](README.zh-CN.md)
