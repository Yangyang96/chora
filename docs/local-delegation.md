# Local Agent delegation

[简体中文](local-delegation.zh-CN.md) · [Roadmap](../ROADMAP.md)

M3 starts with one person assigning research work to several Agent executions on
one Mac. This first slice supports material-only research and document Tasks in a
software Project. Each assignment becomes a normal Task in the parent's Room,
with its own Run, workspace, result and review history.

## Use

Open an unarchived, open research Task and use **Agent delegation**. Enter one
to four assignments, each with a distinct role, a title and instructions. Select
**Start delegation** to authorize their sequential execution. This is a separate
explicit action: creating or starting an ordinary Task never implicitly delegates.
The parent does not need to execute its own Agent Run first.

The plan becomes immutable when started. Role names identify assignments; they
are not user accounts, credential identities or reusable Agent profiles. Children
inherit the parent's exact supplied material, selected context revisions,
execution environment, model selection and frozen native capability configuration.
Later Project edits do not rebind them. Local execution still requires the current
No Sandbox acknowledgement. Unavailable execution fails closed with no host or
model fallback. Runtime-default model selection remains a default, not a promise
of a particular observed model.

Chora activates each unchanged generated execution plan with system provenance,
then starts the normal Pi Run. Execution is sequential within a delegation. The
existing bounded automatic retry policy applies to each child: at most two
automatic retries after its initial Attempt. Children cannot delegate again.
Starting additional independent delegations is a separate user action; this is
not a machine-wide concurrency or token-spending budget.

## Use a plan proposed by the parent Agent

A research Agent can propose the assignments instead of requiring you to type
all of them. Ask the parent Task to include exactly one standalone block with
this format in its complete final Markdown result:

````text
```chora-delegation-plan
{"schemaVersion":"chora.delegation-plan.v1","assignments":[{"role":"Researcher","title":"Compare options","requirement":"Compare the supplied options; cite sources and mark unknowns."}]}
```
````

While that parent Task is still open, choose **Load Agent plan**. Review the
assignments and **Plan source**, then choose **Start proposed delegation** to
explicitly authorize execution. Loading or generating a proposal never starts
children, saves an accepted document, or constitutes human result acceptance.
This remains a proposal-and-start workflow. Dedicated research delegation below
records its separate execution authority before the planning Run starts.

The server imports only the latest Run's current, review-ready document result.
It checks the complete assistant event, Agent report, result and text digests,
and rejects a stale source at start. The plan must contain one to four distinct
roles and fit within 32 KiB. Unknown fields, duplicate JSON keys, multiple plan
blocks, trailing JSON and invalid text are rejected. The format cannot select
resources, execution environments, models, credentials or further delegation.
Invalid proposals create no children and trigger no automatic repair or replanning.

Starting freezes the source Run, Attempt, result, report and assistant event,
along with result, text and plan digests. The browser sends the source identity
and expected result digest; the server reloads and validates the assignments.
Later parent retries or result changes do not replace the imported plan, and the
collection shows when its source is no longer current. Stop and restart retain
the original frozen plan and the same child execution limits.

## Plan and execute with one start

When creating a Task in the local Workbench, choose **Plan and delegate research**,
enter the research goal and supplied materials, then choose **Start research
delegation**. The Task freezes an explicit planning requirement before its normal
execution plan is activated. This start records durable authority for one planning
Attempt and up to four sequential research assignments from its valid result.
The same action is available on an unstarted dedicated planning Task.

Chora binds that authority to one parent Run and Attempt. It consumes only that
Run's complete, current, source-checked proposal and atomically freezes the plan
before child execution. Ordinary research results and text containing the plan
format do not grant this authority. Manual plans, imported proposals and planning
intents are mutually exclusive for a parent Task.

The planning Attempt is not automatically retried. A failed or malformed plan
blocks without creating children, repairing the output or starting a new planning
round. Child execution retains its existing bounded retry policy. **Stop research
planning** records stop intent immediately and prevents automatic import before
cancelling the bound planning Run. After import, use **Stop delegation** for the
child execution phase. An interrupted planning phase becomes blocked on restart;
inspect the existing Run before choosing **Resume research planning**. Resume
keeps the same authorization and identities and does not grant another Attempt.
Use a new Task if a new planning round is needed.

## Optional synthesis report

Before starting, select **Generate one synthesis after research** to authorize
one additional Agent Attempt after every assignment has a complete result. This
option is available for manual assignments, source-bound proposals and single-start
planning. It defaults off and cannot be added to an already-started delegation.

Chora freezes the complete original child results, their identities and digests
as the only materials of a separate synthesis Task. Each original result must
fit within 64 KiB; oversized input blocks rather than being silently truncated.
The synthesis inherits the
parent's frozen execution and model settings. Chora selects no repositories and
supplies no project context or additional external material. The synthesis
instructions prohibit further research; inherited runtime capabilities remain
unchanged. Its report must preserve
source attribution, disagreements and unknowns rather than treating child claims
as verified facts. The report is Agent-authored and remains subject to human review.

The authorization permits one synthesis Task, Run and Attempt, with no automatic
retry, repair or regeneration. The parent remains running until the synthesis
produces a reviewable result; a failure blocks the workflow. **Stop delegation**
also stops this phase. Restart and explicit recovery preserve the same identities.
If a child result changes later, the collection marks the frozen report's sources
as stale without replacing its original inputs or regenerating the report.

## Results and review

The parent shows each child's current state and a link to its normal execution
and review screen. Completed findings show their original Markdown, result
identity and digest. By default this is a collection of independently sourced
results. An additional Agent-written report is produced only when synthesis was
explicitly authorized before start, and has its own Task and source lineage.
A role labelled "Reviewer" still produces an Agent proposal.

**Awaiting review** means every assignment and any authorized synthesis have
finished with a reviewable result.
It does not mean the research is factually verified or accepted. Inspect evidence,
unknowns and individual results before using the existing document review actions.
Delegation never saves an accepted Room revision, accepts a result, starts a later
implementation Task, commits, pushes or merges changes. If a child later changes
state, the collection exposes that it needs attention again.

## Stop and recovery

**Stop delegation** records the stop intent before cancelling an active child and
prevents the next automatic child launch. A child prepared without a runtime
session can be safely cancelled. An already-running check is allowed to settle;
an uncertain stop stays visible rather than claiming that execution has ended.
Stopping does not cancel an unrelated parent Run or delete existing work.

After Workbench restart, running delegations become blocked. Inspect their child
Runs, resolve any recovery state, then explicitly choose **Resume delegation**.
An interrupted stop remains a stop. Stable child and Run identities, command
idempotency and transactional parent links prevent duplicate children or Runs.
Resuming does not reset the automatic retry allowance or authorize a changed
execution plan. A stopped plan is retained as history; use a new parent Task for
a new delegation.

## Current boundary

This is the first local M3 slice, not complete team collaboration. Multi-human
identity/membership/revocation, recursive delegation,
parallel child execution, coding assignments and code integration remain future
work. Remote placement, a general background queue, notifications and token/cost
budgets remain demand-gated M4 work. The existing service owns these local Runs;
closing a browser tab does not transfer execution to another machine.

Verification must distinguish credential-free domain/API/persistence/browser
checks from real-model journeys. Technical completion does not substitute for
final product user acceptance or qualify signed macOS distribution.
