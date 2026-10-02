/** REST 统一响应包络 */
export interface ApiOk<T> {
  ok: true;
  data: T;
}

export interface ApiError {
  ok: false;
  statusCode: number;
  message: string;
  /** 稳定错误码，便于前端分支处理 */
  code?: string;
  details?: unknown;
}

export type ApiResponse<T> = ApiOk<T> | ApiError;

/** GET /api/health */
export interface HealthResponse {
  status: 'ok';
  name: string;
  version: string;
  uptimeSec: number;
  timestamp: string;
}
