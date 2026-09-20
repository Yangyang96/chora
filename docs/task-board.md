# Task board

[简体中文](task-board.zh-CN.md) · [Roadmap](../ROADMAP.md)

The Tasks view brings existing Tasks from a Project's Rooms together. Open
**Tasks** from a Project, or **Task board** from a Room. Switch between Board and
List, filter by Room, repository or phase, and use **Needs attention** to find
work requiring a decision, review, recovery or delivery. New Task still opens the
existing creation flow; a Project uses its default Room.

## Reading the board

A card is one Task, including its multiple Runs, Attempts and repositories.
The five groups are derived from retained work evidence:

| Phase | Meaning |
| --- | --- |
| Preparing | Initial planning/preparation, or a supported return to planning. |
| Working | Execution, checking, stopping, retry or recovery; not necessarily a running Agent. |
| Review | A result needs review. Plan review stays in Preparing. |
| Delivery | Accepted changes still need Commit, Push, PR, Merge or local Apply. |
| Finished | A proved end outcome. Read the outcome: finished does not always mean success. |

Accepted changes are not delivered until all applicable repositories reach their
supported delivery endpoint. Merge, local Apply, mixed delivery, no changes,
closure and partial delivery with remaining work closed have distinct labels.
A cancelled Run can still belong to a resumable Task. Cleanup is separate from
delivery: a cleanup failure needs attention without undoing a proved merge.

**Needs reconciliation** keeps unavailable or contradictory evidence visible
outside the five phases. It does not guess that work is finished. Details retain
the evidence and existing recovery actions. An archived Task is hidden by default;
select **Include archived** to read it. Archived Projects and Rooms remain
read-only and do not authorize new work.

## Navigation and freshness

Card links open existing Task/Run pages, which reload current state before offering
commands. The board does not directly run, accept, merge, archive or clean up work.
Filtering, reading and refreshing make no model calls and never create worktrees.
Filters and Board/List choice are stored in the URL and survive back navigation.
Keyboard controls and a stacked narrow-window layout provide the same actions.

The visible view refreshes about every two seconds, with one board request at a
time. Hidden views stop polling; focus, reconnect and return refresh the view.
A failed refresh retains the last observed cards and marks them stale. Failure
before the first successful read shows unavailable, not an empty Project.

Room and repository filters load at most 100 choices of each kind per response,
plus a selected choice outside that page. Use **Load more filters** to reach later
choices. Their pagination is independent of Task pages and ongoing Task progress.

Pages contain 50 cards. Counts cover the complete filtered set, not only loaded
cards. Load more to continue; if evidence changes, pagination restarts rather
than combining incompatible snapshots. Viewing the board does not stop Agent
work, authorized checks or app previews. Closing a browser tab retains the macOS
application service; normal application quit retains its existing stop/recovery
rules. Reopening does not automatically restart previews or interrupted work.

## V0 scope

V0 organizes existing work. Draft backlogs, manual priority/order, dragging to
change state, assignment, dependency scheduling and external tracker sync are
outside this version. Follow the [plan](task-board-plan.md) for later directions
and the recorded implementation/verification status. Formal signed macOS
installation and updates are separate from task-board qualification.
