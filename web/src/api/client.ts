import { ApiError } from './types'
import type { APIErrorEnvelope } from './types'
import { getToken, notifyUnauthorized } from './authState'

export { ApiError }

export const API_BASE = '/api/v1'

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  /** JSON body (mutually exclusive with `form`). */
  body?: unknown
  /** multipart/form-data body. */
  form?: FormData
  query?: Record<string, string | number | boolean | undefined | null>
  signal?: AbortSignal
  /** Set to false for endpoints that must not trigger the 401 bounce. */
  redirectOn401?: boolean
  headers?: Record<string, string>
}

function buildUrl(path: string, query?: RequestOptions['query']): string {
  const url = `${API_BASE}${path.startsWith('/') ? path : `/${path}`}`
  if (!query) return url
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue
    search.append(key, String(value))
  }
  const qs = search.toString()
  return qs ? `${url}?${qs}` : url
}

function isErrorEnvelope(value: unknown): value is APIErrorEnvelope {
  if (typeof value !== 'object' || value === null) return false
  const err = (value as { error?: unknown }).error
  if (typeof err !== 'object' || err === null) return false
  return typeof (err as { message?: unknown }).message === 'string'
}

async function parseBody(response: Response): Promise<unknown> {
  const text = await response.text()
  if (!text) return null
  const type = response.headers.get('content-type') ?? ''
  if (type.includes('application/json') || text.startsWith('{') || text.startsWith('[')) {
    try {
      return JSON.parse(text) as unknown
    } catch {
      return text
    }
  }
  return text
}

/** Extract a human message from an error envelope, a raw string, or a 501 stub. */
function describeFailure(status: number, payload: unknown, statusText: string): ApiError {
  if (isErrorEnvelope(payload)) {
    const { code, message, details } = payload.error
    return new ApiError(status, code || `http_${status}`, message, details)
  }
  if (typeof payload === 'string' && payload.trim()) {
    return new ApiError(status, `http_${status}`, payload.trim().slice(0, 400))
  }
  if (status === 501) {
    return new ApiError(501, 'not_implemented', '该接口在本版本中尚未实现（501）')
  }
  if (status === 401) {
    return new ApiError(401, 'unauthorized', '登录状态已失效，请重新登录')
  }
  if (status === 403) {
    return new ApiError(403, 'forbidden', '权限不足')
  }
  return new ApiError(status, `http_${status}`, statusText || `请求失败（HTTP ${status}）`)
}

/**
 * Perform a typed API request against `/api/v1`.
 *
 * - injects `Authorization: Bearer <jwt>` from the auth state
 * - parses the `{"error":{"code","message","details"}}` envelope on failure
 * - redirects to /login on 401 (unless `redirectOn401: false`)
 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, form, query, signal, redirectOn401 = true, headers } = options

  const finalHeaders: Record<string, string> = { Accept: 'application/json', ...headers }
  const token = getToken()
  if (token) finalHeaders.Authorization = `Bearer ${token}`

  let payload: BodyInit | undefined
  if (form) {
    payload = form
  } else if (body !== undefined) {
    finalHeaders['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }

  let response: Response
  try {
    response = await fetch(buildUrl(path, query), {
      method,
      headers: finalHeaders,
      body: payload,
      signal,
      credentials: 'same-origin',
    })
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause
    throw new ApiError(0, 'network_error', '无法连接到面板服务，请确认面板进程是否在运行')
  }

  const parsed = await parseBody(response)

  if (!response.ok) {
    const error = describeFailure(response.status, parsed, response.statusText)
    if (response.status === 401 && redirectOn401) notifyUnauthorized()
    throw error
  }

  return parsed as T
}

export const api = {
  get: <T>(path: string, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...options, method: 'GET' }),
  post: <T>(path: string, body?: unknown, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...options, method: 'POST', body }),
  put: <T>(path: string, body?: unknown, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...options, method: 'PUT', body }),
  del: <T>(path: string, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...options, method: 'DELETE' }),
  upload: <T>(path: string, form: FormData, options?: Omit<RequestOptions, 'method' | 'form'>) =>
    request<T>(path, { ...options, method: 'POST', form }),
}

/**
 * Best-effort error text for toasts. Never throws.
 */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message
  if (err instanceof Error) return err.message
  return String(err)
}

/**
 * True when the failure means "this build does not implement the route yet".
 * Pages use it to render a "not available in this build" empty state.
 */
export function isUnavailable(err: unknown): boolean {
  return err instanceof ApiError && err.isUnavailable
}
