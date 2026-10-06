import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { endpoints } from '@/api/endpoints'
import { ApiError, errorMessage, isUnavailable } from '@/api/client'
import { parseEventFrame, openEventSocket, type ReconnectingSocket, type SocketState } from '@/api/ws'
import type { CreateInstanceRequest, InstanceStats, InstanceStatus, InstanceSummary } from '@/api/types'

export const useInstancesStore = defineStore('instances', () => {
  const instances = ref<InstanceSummary[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  /** True when /instances answered 501 — this build has no instance API yet. */
  const unavailable = ref(false)

  const eventSocket = ref<ReconnectingSocket | null>(null)
  const eventState = ref<SocketState>('closed')
  const liveStats = ref<Record<string, InstanceStats>>({})

  const running = computed(() => instances.value.filter((i) => i.status === 'running'))
  const totalPlayers = computed(() =>
    instances.value.reduce((sum, i) => sum + (i.players_online ?? 0), 0),
  )

  function byId(id: string): InstanceSummary | undefined {
    return instances.value.find((i) => i.id === id)
  }

  function patch(id: string, changes: Partial<InstanceSummary>): void {
    const index = instances.value.findIndex((i) => i.id === id)
    if (index < 0) return
    instances.value[index] = { ...instances.value[index], ...changes }
  }

  async function fetchAll(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      instances.value = (await endpoints.listInstances()) ?? []
      unavailable.value = false
    } catch (err) {
      instances.value = []
      if (isUnavailable(err)) {
        unavailable.value = true
        error.value = null
      } else if (err instanceof ApiError && err.status === 401) {
        // Session expiry is handled by the client's 401 hook; stay quiet here.
        error.value = null
      } else {
        error.value = errorMessage(err)
      }
    } finally {
      loading.value = false
    }
  }

  async function fetchOne(id: string): Promise<InstanceSummary> {
    const detail = await endpoints.getInstance(id)
    patch(id, detail)
    return detail
  }

  async function create(payload: CreateInstanceRequest): Promise<InstanceSummary> {
    const created = await endpoints.createInstance(payload)
    instances.value = [...instances.value, created]
    return created
  }

  async function remove(id: string, removeDir = false): Promise<void> {
    await endpoints.deleteInstance(id, removeDir)
    instances.value = instances.value.filter((i) => i.id !== id)
  }

  async function start(id: string): Promise<void> {
    optimisticStatus(id, 'starting')
    try {
      await endpoints.startInstance(id)
    } catch (err) {
      await fetchAll()
      throw err
    }
  }

  async function stop(id: string, force = false): Promise<void> {
    optimisticStatus(id, 'stopping')
    try {
      await endpoints.stopInstance(id, { force })
    } catch (err) {
      await fetchAll()
      throw err
    }
  }

  async function restart(id: string): Promise<void> {
    optimisticStatus(id, 'starting')
    try {
      await endpoints.restartInstance(id)
    } catch (err) {
      await fetchAll()
      throw err
    }
  }

  function optimisticStatus(id: string, status: InstanceStatus): void {
    patch(id, { status })
  }

  function setStatus(id: string, status: InstanceStatus): void {
    patch(id, { status })
  }

  function setStats(id: string, stats: InstanceStats): void {
    liveStats.value = { ...liveStats.value, [id]: stats }
    patch(id, {
      cpu_percent: stats.cpu_percent,
      memory_bytes: stats.memory_bytes,
      players_online: stats.players_online,
      players_max: stats.players_max ?? byId(id)?.players_max,
      uptime_seconds: stats.uptime_seconds,
      status: stats.status ?? byId(id)?.status,
    })
  }

  /** Subscribe to /ws/events so status lights update without polling. */
  function connectEvents(): void {
    if (eventSocket.value) return
    eventSocket.value = openEventSocket({
      onState: (state) => {
        eventState.value = state
      },
      onMessage: (raw) => {
        const frame = parseEventFrame(raw)
        if (!frame) return
        if (frame.type === 'instance_status') setStatus(frame.instance_id, frame.status)
        if (frame.type === 'instance_stats') setStats(frame.instance_id, frame.stats)
      },
    })
  }

  function disconnectEvents(): void {
    eventSocket.value?.close()
    eventSocket.value = null
    eventState.value = 'closed'
  }

  function reset(): void {
    disconnectEvents()
    instances.value = []
    liveStats.value = {}
    error.value = null
    unavailable.value = false
  }

  return {
    instances,
    loading,
    error,
    unavailable,
    eventState,
    liveStats,
    running,
    totalPlayers,
    byId,
    patch,
    fetchAll,
    fetchOne,
    create,
    remove,
    start,
    stop,
    restart,
    setStatus,
    setStats,
    connectEvents,
    disconnectEvents,
    reset,
  }
})
