import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { clearToken, readCachedUser, setToken, writeCachedUser } from '@/api/authState'
import type { User } from '@/api/types'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(readCachedUser<User>())
  const token = ref<string>('')
  const loading = ref(false)
  const error = ref<string | null>(null)
  /** true once /auth/me has been attempted (successfully or not). */
  const resolved = ref(false)

  const isAuthenticated = computed(() => Boolean(user.value))
  const isAdmin = computed(() => user.value?.role === 'admin')
  /** Roles allowed to mutate instances; viewers are read-only. */
  const canOperate = computed(
    () => user.value?.role === 'admin' || user.value?.role === 'operator',
  )

  function applySession(payload: { token: string; user: User }): void {
    setToken(payload.token)
    token.value = payload.token
    user.value = payload.user
    writeCachedUser(payload.user)
    resolved.value = true
  }

  async function login(username: string, password: string): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const result = await endpoints.login({ username, password })
      applySession(result)
    } catch (err) {
      error.value = errorMessage(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  async function setup(password: string, username = 'admin'): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const result = await endpoints.setup({ username, password, confirm_password: password })
      applySession(result)
    } catch (err) {
      error.value = errorMessage(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /** Load the current user; tolerates a cold panel that has no setup yet. */
  async function fetchMe(): Promise<boolean> {
    loading.value = true
    try {
      const me = await endpoints.me()
      user.value = me
      writeCachedUser(me)
      return true
    } catch (err) {
      if (!isUnavailable(err)) {
        user.value = null
        writeCachedUser(null)
        clearToken()
      }
      return false
    } finally {
      resolved.value = true
      loading.value = false
    }
  }

  async function logout(): Promise<void> {
    try {
      await endpoints.logout()
    } catch {
      // Logging out locally is enough even if the panel is unreachable.
    }
    reset()
  }

  function reset(): void {
    clearToken()
    token.value = ''
    user.value = null
    writeCachedUser(null)
  }

  return {
    user,
    token,
    loading,
    error,
    resolved,
    isAuthenticated,
    isAdmin,
    canOperate,
    login,
    setup,
    fetchMe,
    logout,
    reset,
  }
})
