# Skills and MCP in local execution

[简体中文](native-capabilities.zh-CN.md)

Project **Skills and MCP** manages references to existing local Pi resources.
It applies to new Tasks using local execution in every Room of that Project.
Global Pi files and other Projects are unchanged. Isolated execution does not load
these resources and has no Skills/MCP configuration support in this slice.

The qualified runtime is Pi 0.85.1 with the optional
[`pi-mcp-adapter@2.34.0`](https://github.com/nicobailon/pi-mcp-adapter/releases/tag/v2.34.0)
extension. Pi has no built-in MCP client. Install the bridge yourself through
Pi before using MCP management; Chora neither installs nor updates packages.
The bridge's package directory can be supplied explicitly when discovery cannot
find it in the enabled Pi extensions.

## Configure a Project

Open a Project and expand **Skills and MCP**. Add absolute paths to existing
Skill files/directories, an optional installed bridge directory and an optional
existing MCP configuration file. Use **Disabled Skill paths** for the exact
Skill file paths shown by discovery; use **Disabled MCP server names** for
server identifiers. The first save enables Project-managed capability loading
for future Tasks; Projects without a saved configuration keep their existing
native Pi loading behavior. Save the configuration before verifying it.

Global resources are inherited by default. Project disable entries affect only
this Project; removing an entry restores inheritance. Native repository-local
discovery can differ between task workspaces. The Project discovery view shows
global resources and the explicit Project references; each execution's observed
state describes its own workspace. These local extensions run with the existing
local execution's **No Sandbox** permissions. External instructions and tool
responses do not gain authority over the task's contract.

Chora stores references and disable entries, not the referenced contents or
credentials. Configure authentication in the existing Provider/bridge setup or
environment. Chora has no MCP credential form or OAuth login flow. A server
needing authentication stays unavailable until that setup is repaired.

## Inspect and recover

**Refresh status** reads configuration without connecting servers or invoking
extensions. **Discovered**, **cached**, and **not connected** are not claims of
an active connection. An intentionally disabled resource is different from an
enabled resource that failed to load.

**Verify / retry connections** starts a temporary Pi session and uses the native
bridge reconnect command. It may start configured local MCP processes or connect
to configured remote servers. It does not call a model, invoke business tools,
or reload an already running execution. Results are observations of that check,
not a promise that a later task will have the same connections.

A failed or authentication-required server is unavailable; other capabilities
can continue. The Project view shows per-execution observations, and the task
view warns when its recorded capabilities are unavailable. Repair the source
configuration or authentication and retry verification. Changes take effect on
new Tasks. Tasks capture these Project references at creation; retries keep the
original references. All enabled capabilities remain available for explicit user
invocation or the Agent's own selection, without a Task-level capability picker. An explicit session resume reuses its original Project settings
and refuses changed resource/configuration fingerprints; start a fresh execution
when those resources have changed. Fingerprints cover loaded Skill text and
qualified bridge runtime sources; they do not certify arbitrary third-party
extension dependencies or external server implementations. A stopped execution's state is last observed
history, not a live connection.

## Verification

Configuration tests cover persistence, optimistic concurrency, invalid inputs,
archive guards and Project isolation. Browser tests cover the configuration
journey. Native integration can be run with locally installed qualified packages:

```sh
CHORA_TEST_CAPABILITY_PI="$(command -v pi)" \
CHORA_TEST_CAPABILITY_BRIDGE="/absolute/path/to/pi-mcp-adapter" \
go test ./internal/nativecapabilities -run 'TestRealPi' -v
```

The tests use disposable state and check Skill availability, inherited and
Project MCP configuration, and healthy, failed and disabled servers without
model credentials. Without those environment variables
it is explicitly skipped; ordinary unit tests do not certify a local installation.
