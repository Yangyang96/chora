// Shared API client helpers. Extracted from App.tsx during the UI rewrite.

export class APIError extends Error {
  status: number
  constructor(status: number, detail: string) {
    super(detail)
    this.status = status
  }
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...init })
  const body = await response.json()
  if (!response.ok) {
    const failure = body?.failure
    const detail = [failure?.observed, failure?.required, failure?.action].filter((value) => typeof value === 'string' && value.trim()).join(' · ')
    throw new APIError(response.status, body?.error || detail || `Request failed: ${response.status}`)
  }
  return body as T
}

export function message(reason: unknown) {
  return reason instanceof Error ? reason.message : String(reason)
}

export function commandKey(prefix: string) {
  return `${prefix}-${crypto.randomUUID()}`
}
