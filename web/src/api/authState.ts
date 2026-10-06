import { reactive } from 'vue'

const TOKEN_KEY = 'scnetm.token'
const USER_KEY = 'scnetm.user'

/**
 * Token holder shared by the HTTP client and the WebSocket helper.
 *
 * It lives outside the Pinia store on purpose: `api/client.ts` and `api/ws.ts`
 * must not import the store layer (that would create a store → api → store
 * cycle). The Pinia auth store writes here and stays the single source of truth
 * for the UI.
 */
export const tokenState = reactive<{ token: string }>({
  token: readToken(),
})

function readToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? ''
  } catch {
    return ''
  }
}

export function getToken(): string {
  return tokenState.token
}

export function setToken(token: string): void {
  tokenState.token = token
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token)
    else localStorage.removeItem(TOKEN_KEY)
  } catch {
    /* private mode — keep it in memory only */
  }
}

export function clearToken(): void {
  setToken('')
}

export function readCachedUser<T>(): T | null {
  try {
    const raw = localStorage.getItem(USER_KEY)
    return raw ? (JSON.parse(raw) as T) : null
  } catch {
    return null
  }
}

export function writeCachedUser(user: unknown): void {
  try {
    if (user) localStorage.setItem(USER_KEY, JSON.stringify(user))
    else localStorage.removeItem(USER_KEY)
  } catch {
    /* ignore */
  }
}

/** Callback installed by the router so a 401 can bounce to /login. */
type UnauthorizedHandler = () => void
let onUnauthorized: UnauthorizedHandler | null = null

export function setUnauthorizedHandler(handler: UnauthorizedHandler): void {
  onUnauthorized = handler
}

export function notifyUnauthorized(): void {
  clearToken()
  writeCachedUser(null)
  onUnauthorized?.()
}
