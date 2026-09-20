# Optional app preview

[简体中文](app-preview.zh-CN.md)

Code, diffs, checks and Review remain the normal acceptance workflow. App preview
is an optional way to inspect a Web app after the Agent finishes. It does not
approve a result, count as a passing check or become a delivery requirement.

## Use a preview

1. Open the latest Task run and expand **App preview**.
2. Select a writable Task repository. Chora reads `package.json` for `dev` and
   `start` suggestions without executing them. You can enter another command.
3. Check the command, working directory relative to that repository, and app
   port. Save the configuration or select **Start preview** explicitly.
4. Open the preview in a separate browser tab and read its logs in Chora.
5. Select **Stop preview** when finished. Preview cleanup removes its temporary
   resources while retaining the saved configuration; it does not delete code.

The configured port must match the application. Chora provides `PORT` and `HOST`
environment variables, but some frameworks require explicit CLI arguments. For
example, Vite can use `npm run dev -- --host "$HOST" --port "$PORT" --strictPort`.
The command runs through a shell. Review a discovered suggestion before starting
it; a suggestion is not proof that the app or its dependencies are ready.

Dependencies are not installed automatically. If startup fails, inspect the logs
and fix the command or prepare the necessary dependencies in the appropriate
environment. A running process does not prove that the application is ready or
correct. The browser and logs show the actual result.

## Execution modes

**Local execution · No Sandbox** runs the explicit command in the proven Task worktree with
the host's tools and environment. It is **No Sandbox**. Configure the app to
listen on loopback. Chora opens a loopback URL, but the application command still
controls its own network listeners and can modify files as any local command can.
Preview-generated changes remain subject to the existing Review and delivery
checks; they do not become previously reviewed code.

**Isolated execution** uses the prepared, verified image in a separate application
container. The Agent container's policy stays unchanged. Preview publishes one
port on `127.0.0.1`; the host port may differ from the configured app port. Inside
the container the app must listen on `0.0.0.0` to accept that forwarded connection.
The container reads a private source snapshot and runs in a disposable writable
workspace, with no writable host mount or automatic credential projection. It
has a read-only root filesystem and bounded memory, CPU and process resources.
Docker bridge egress remains available. There is no fallback to host execution.

An isolated preview is a snapshot taken at startup. Stop and start it again to
inspect newer code. Its generated files are discarded rather than imported as
Agent output. This first slice manages one app process per Task repository, not a
multi-service deployment or arbitrary project-specific container images.

## Lifetime and recovery

Navigating away or closing the browser tab does not stop the app. Explicit stop
and normal Workbench shutdown stop previews. Starting a successor Attempt or
cleaning Task worktrees stops the affected Task's previews first.

After an abnormal exit, Chora checks durable process or container ownership and
offers recovery cleanup. It never automatically restarts the app. If ownership
cannot be verified, Chora refuses to kill an unrelated process or remove an
unverified container. Restore the original environment or resolve the reported
residue before starting another preview.

Configuration, ownership records and bounded logs live in Chora's private data
root. They are local operational state, not repository files or acceptance
evidence. Follow the [maintenance guide](workbench-maintenance.md) for backup and
restore with Workbench stopped.
