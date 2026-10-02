import type { ApiError, ApiResponse } from '@sc-panel/shared';

/** 后端 /api 基础地址（经 Nitro 代理到 NestJS，同源） */
const API_BASE = '/api';

export class ApiRequestError extends Error {
  statusCode: number;
  code?: string;
  details?: unknown;
  constructor(err: ApiError) {
    super(err.message);
    this.name = 'ApiRequestError';
    this.statusCode = err.statusCode;
    this.code = err.code;
    this.details = err.details;
  }
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  isForm = false,
): Promise<T> {
  const init: RequestInit = {
    method,
    credentials: 'include',
    headers: {},
  };
  if (body !== undefined) {
    if (isForm) {
      init.body = body as FormData;
    } else {
      (init.headers as Record<string, string>)['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
  }

  const res = await fetch(`${API_BASE}${path}`, init);
  const text = await res.text();
  const json = text ? (JSON.parse(text) as ApiResponse<T>) : undefined;

  if (!res.ok || (json && json.ok === false)) {
    const err: ApiError =
      json && json.ok === false
        ? json
        : { ok: false, statusCode: res.status, message: res.statusText || '请求失败' };
    throw new ApiRequestError(err);
  }
  return (json as { ok: true; data: T }).data;
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
  upload: <T>(path: string, form: FormData) => request<T>('POST', path, form, true),
};
