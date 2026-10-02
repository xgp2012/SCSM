/** 单管理员认证相关 DTO（plants.md §5.1） */

export interface LoginRequest {
  username: string;
  password: string;
}

export interface AuthUser {
  username: string;
  role: 'admin';
}

export interface LoginResponse {
  user: AuthUser;
}

export interface MeResponse {
  user: AuthUser;
}

export const AUTH_COOKIE_NAME = 'sc_panel_token';
