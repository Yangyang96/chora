// This intent is part of the Task requirement before its contract is frozen.
// Only an explicit Start command grants execution authority.
export const DELEGATION_PLANNING_MARKER = '[Chora research delegation planning v1]'

export function isDelegationPlanningTask(requirement?: string): boolean {
  return Boolean(requirement?.includes(DELEGATION_PLANNING_MARKER))
}

export function delegationPlanningRequirement(requirement: string): string {
  return `${requirement.trim()}\n\n${DELEGATION_PLANNING_MARKER}
Plan bounded research delegation for the goal above using only the frozen supplied materials. Do not execute the assignments or modify repositories. Return one complete final Markdown response containing exactly one fenced code block with language chora-delegation-plan. Its JSON must have exactly two fields: "schemaVersion": "chora.delegation-plan.v1" and "assignments": an array of one to four objects. Each assignment must contain only "role", "title", and "requirement" strings. Roles must be distinct (up to 80 characters), titles up to 256 characters, and requirements up to 4000 characters. Give each assignment a useful, bounded research question; require concise sourced Markdown that distinguishes supplied facts, analysis, and unknowns. Assignments use the same frozen materials and execution settings. No recursive delegation, external research, code changes, delivery, or automatic acceptance. Chora will execute a valid plan sequentially under the explicit start authorization; final results remain for human review.`
}
