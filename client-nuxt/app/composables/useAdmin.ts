/**
 * useAdmin — composable for all rdio-scanner admin REST API calls.
 */

import { ref, computed, readonly } from 'vue'

export interface AdminConfig {
  access: AccessConfig[]
  apikeys: ApiKey[]
  bridge: BridgeConfig
  dirwatch: DirwatchEntry[]
  downstreams: Downstream[]
  groups: Group[]
  options: Options
  systems: AdminSystem[]
  tags: Tag[]
  docker: boolean
}

export interface AccessConfig {
  id?: number
  code: string
  expiration?: string
  ident: string
  limit?: number
  order?: number
  // "*" = all systems; a plain {} map is denied by the server (see ApiKey).
  systems: '*' | Record<number, Record<number, boolean>>
}

export interface ApiKey {
  id?: number
  disabled: boolean
  ident: string
  key: string
  order?: number
  // Server grants access on "*" (all systems) or a [{id, talkgroups}] list.
  // A plain object/{} denies everything, so new keys default to "*".
  systems: '*' | Record<number, Record<number, boolean>>
}

export interface SDRDeviceAssignment {
  index: number
  serialNumber: string
  assignTo: '' | 'sdrangel' | 'trunk-recorder'
}

export interface BridgeConfig {
  enabled: boolean
  host: string
  port: number
  channels: BridgeChannel[]
  sdrangelBinaryPath: string
  sdrangelContainerName: string
  trunkRecorderBinaryPath: string
  trunkRecorderContainerName: string
  trunkRecorderConfigPath: string
  sdrDeviceAssignments: SDRDeviceAssignment[]
}

export interface BridgeChannel {
  channelIndex: number
  deviceSetIndex: number
  frequencyHz: number
  label: string
  protocol: string
  sampleRate: number
  systemRef: number
  talkgroupRef: number
  udpPort: number
}

export interface BridgeStatus {
  running: boolean
  channelCount: number
  mode: string
}

export interface SDRangelServiceStatus {
  running: boolean
  mode: string
  message?: string
  containerId?: string
  containerName?: string
  pid?: number
  startedAt?: string
  uptimeSeconds?: number
}

export interface SDRangelServiceResult {
  success: boolean
  message: string
}

export interface TrunkRecorderServiceStatus {
  running: boolean
  mode: string
  message?: string
  pid?: number
}

export interface TrunkRecorderServiceResult {
  success: boolean
  message: string
}

export interface DirwatchEntry {
  id?: number
  directory: string
  disabled: boolean
  extension?: string
  frequency?: number
  mask?: string
  order?: number
  systemId?: number
  talkgroupId?: number
  type: 'default' | 'dsdplus' | 'sdr-trunk' | 'trunk-recorder'
  deleteAfter?: boolean
}

export interface Downstream {
  id?: number
  apiKey: string
  disabled: boolean
  order?: number
  // "*" = forward all systems; a plain {} map forwards nothing (see ApiKey).
  systems: '*' | Record<number, boolean>
  url: string
}

export interface Group {
  id?: number
  label: string
}

export interface Tag {
  id?: number
  label: string
}

export interface Options {
  audioConversion: number
  autoPopulate: boolean
  branding: string
  dimmerDelay: number
  disableDuplicateDetection: boolean
  duplicateDetectionTimeFrame: number
  email: string
  keypadBeeps: string
  maxClients: number
  playbackGoesLive: boolean
  pruneDays: number
  showListenersCount: boolean
  sortTalkgroups: boolean
  time12hFormat: boolean
}

export interface AdminSystem {
  id?: number
  alert: string
  autoPopulate: boolean
  blacklists: string
  delay: number
  label: string
  led: string
  order?: number
  systemRef: number
  type: string
  sites: AdminSite[]
  talkgroups: AdminTalkgroup[]
  units: AdminUnit[]
}

export interface AdminSite {
  id?: number
  label: string
  order?: number
  siteRef: number
}

export interface AdminTalkgroup {
  id?: number
  alert: string
  delay: number
  frequency?: number
  groupIds: number[]
  label: string
  led: string
  name: string
  order?: number
  tagId?: number
  talkgroupRef: number
  type: string
}

export interface AdminUnit {
  id?: number
  label: string
  order?: number
  unitRef: number
}

export interface AlertEntry {
  dateTime: string
  level: string
  message: string
}

export interface LogEntry {
  dateTime: string
  level: string
  message: string
}

export interface TodoEntry {
  label: string
  count: number
}

export interface SDRangelConnectStatus {
  connected: boolean
  version?: string
  os?: string
  deviceSets?: Array<{
    index: number
    hwType: string
    channels: Array<{ index: number; idText: string; title: string }>
  }>
}

export interface RTLDongle {
  index: number
  manufacturer: string
  product: string
  serialNumber: string
}

const TOKEN_KEY = 'rdio-admin-token'

// Module-level singleton — survives Vue Router navigation and component teardown.
// Safe in client-only (ssr: false) mode: one JS context per browser tab.
const _token = ref('')
if (typeof window !== 'undefined') {
  _token.value = localStorage.getItem(TOKEN_KEY) ?? ''
}

export const useAdmin = () => {
  const toast = useToast()

  const authHeader = () => ({ Authorization: _token.value })

  const handleError = (err: unknown, context: string) => {
    const msg = err instanceof Error ? err.message : String(err)
    toast.add({ title: `${context} failed`, description: msg, color: 'error' })
    console.error(`[admin] ${context}:`, err)
  }

  // ── Auth ───────────────────────────────────────────────────────────────────

  const login = async (password: string): Promise<boolean> => {
    try {
      const res = await $fetch<{ token: string }>('/api/admin/login', {
        method: 'POST',
        body: { password },
      })
      _token.value = res.token
      localStorage.setItem(TOKEN_KEY, res.token)
      return true
    } catch {
      return false
    }
  }

  const logout = async () => {
    try {
      await $fetch('/api/admin/logout', {
        method: 'POST',
        headers: authHeader(),
      })
    } catch { /* ignore */ }
    _token.value = ''
    localStorage.removeItem(TOKEN_KEY)
  }

  const changePassword = async (current: string, next: string): Promise<boolean> => {
    try {
      await $fetch('/api/admin/password', {
        method: 'POST',
        headers: authHeader(),
        body: { currentPassword: current, newPassword: next },
      })
      return true
    } catch (err) {
      handleError(err, 'Change password')
      return false
    }
  }

  const isLoggedIn = computed(() => !!_token.value)

  // ── Config ─────────────────────────────────────────────────────────────────

  const clearAuth = () => {
    _token.value = ''
    localStorage.removeItem(TOKEN_KEY)
  }

  const getConfig = async (): Promise<AdminConfig | null> => {
    try {
      const res = await $fetch<{ config: AdminConfig; docker?: boolean }>('/api/admin/config', {
        headers: authHeader(),
      })
      return { ...res.config, docker: res.docker ?? false }
    } catch (err: unknown) {
      if ((err as Record<string, unknown>)?.statusCode === 401) {
        clearAuth()
      } else {
        handleError(err, 'Load config')
      }
      return null
    }
  }

  const saveConfig = async (cfg: Partial<AdminConfig>): Promise<boolean> => {
    try {
      await $fetch('/api/admin/config', {
        method: 'PUT',
        headers: authHeader(),
        body: cfg,
      })
      toast.add({ title: 'Config saved', color: 'success' })
      return true
    } catch (err) {
      handleError(err, 'Save config')
      return false
    }
  }

  // ── Logs ───────────────────────────────────────────────────────────────────

  const getLogs = async (): Promise<LogEntry[]> => {
    try {
      return await $fetch<LogEntry[]>('/api/admin/logs', {
        headers: authHeader(),
      })
    } catch {
      return []
    }
  }

  // ── Alerts ─────────────────────────────────────────────────────────────────

  const getAlerts = async (): Promise<AlertEntry[]> => {
    try {
      return await $fetch<AlertEntry[]>('/api/admin/alerts', {
        headers: authHeader(),
      })
    } catch {
      return []
    }
  }

  // ── Import: CHIRP CSV ──────────────────────────────────────────────────────

  const importChirp = async (file: File, systemLabel: string, systemRef: number, portBase: number, protocol = '') => {
    const body = new FormData()
    body.append('file', file)
    body.append('systemLabel', systemLabel)
    body.append('systemRef', String(systemRef))
    body.append('portBase', String(portBase))
    if (protocol) body.append('protocol', protocol)
    try {
      return await $fetch('/api/admin/import/chirp', {
        method: 'POST',
        headers: authHeader(),
        body,
      })
    } catch (err) {
      handleError(err, 'CHIRP import')
      return null
    }
  }

  // ── Import: RadioReference CSV ─────────────────────────────────────────────

  const importRRCsv = async (file: File, systemLabel: string, systemRef: number, portBase: number, protocol = '') => {
    const body = new FormData()
    body.append('file', file)
    body.append('systemLabel', systemLabel)
    body.append('systemRef', String(systemRef))
    body.append('portBase', String(portBase))
    if (protocol) body.append('protocol', protocol)
    try {
      return await $fetch('/api/admin/import/rr-csv', {
        method: 'POST',
        headers: authHeader(),
        body,
      })
    } catch (err) {
      handleError(err, 'RadioReference CSV import')
      return null
    }
  }

  // ── Import: TRS (trunked) CSV ──────────────────────────────────────────────

  const importTrsCsv = async (file: File, systemLabel: string, systemRef: number, systemKind = '') => {
    const body = new FormData()
    body.append('file', file)
    body.append('systemLabel', systemLabel)
    body.append('systemRef', String(systemRef))
    if (systemKind) body.append('systemKind', systemKind)
    try {
      return await $fetch('/api/admin/import/trs-csv', {
        method: 'POST',
        headers: authHeader(),
        body,
      })
    } catch (err) {
      handleError(err, 'TRS CSV import')
      return null
    }
  }

  // ── Import: FRS / GMRS presets ────────────────────────────────────────────

  const importFRS = async (systemRef: number, portBase: number) =>
    $fetch('/api/admin/import/frs', {
      method: 'POST',
      headers: authHeader(),
      body: { systemRef, portBase },
    }).catch(err => { handleError(err, 'FRS import'); return null })

  const importGMRS = async (systemRef: number, portBase: number) =>
    $fetch('/api/admin/import/gmrs', {
      method: 'POST',
      headers: authHeader(),
      body: { systemRef, portBase },
    }).catch(err => { handleError(err, 'GMRS import'); return null })

  // ── Import: RadioReference API ────────────────────────────────────────────

  const getRRStates = async (username: string, password: string) =>
    $fetch('/api/admin/import/rr-states', {
      method: 'POST',
      headers: authHeader(),
      body: { username, password },
    }).catch(() => null)

  const getRRCounties = async (username: string, password: string, stateId: number) =>
    $fetch('/api/admin/import/rr-counties', {
      method: 'POST',
      headers: authHeader(),
      body: { username, password, stateId },
    }).catch(() => null)

  const importRRCounty = async (username: string, password: string, countyId: number, systemRef: number, portBase: number) =>
    $fetch('/api/admin/import/rr-county', {
      method: 'POST',
      headers: authHeader(),
      body: { username, password, countyId, systemRef, portBase },
    }).catch(err => { handleError(err, 'RadioReference county import'); return null })

  // ── SDRangel ───────────────────────────────────────────────────────────────

  const getSDRangelStatus = async (): Promise<SDRangelConnectStatus> => {
    try {
      return await $fetch<SDRangelConnectStatus>('/api/admin/sdrangel/status', {
        headers: authHeader(),
      })
    } catch {
      return { connected: false }
    }
  }

  const getSDRangelServiceStatus = async (): Promise<SDRangelServiceStatus> => {
    try {
      return await $fetch<SDRangelServiceStatus>('/api/admin/sdrangel/service', {
        headers: authHeader(),
      })
    } catch {
      return { running: false, mode: 'native', message: 'unavailable' }
    }
  }

  const sdrangelServiceAction = async (action: 'start' | 'stop' | 'restart', binaryPath?: string, args?: string): Promise<SDRangelServiceResult> => {
    try {
      return await $fetch<SDRangelServiceResult>('/api/admin/sdrangel/service/action', {
        method: 'POST',
        headers: authHeader(),
        body: { action, binaryPath: binaryPath ?? '', args: args ?? '' },
      })
    } catch (err) {
      handleError(err, `SDRangel ${action}`)
      return { success: false, message: String(err) }
    }
  }

  const getSDRangelServiceLogs = async (): Promise<string[]> => {
    try {
      return await $fetch<string[]>('/api/admin/sdrangel/service/logs', {
        headers: authHeader(),
      })
    } catch {
      return []
    }
  }

  const provisionSDRangel = async (body: unknown) => {
    try {
      return await $fetch('/api/admin/sdrangel/provision', {
        method: 'POST',
        headers: authHeader(),
        body,
      })
    } catch (err) {
      handleError(err, 'SDRangel provision')
      return null
    }
  }

  // ── Trunk-Recorder service ─────────────────────────────────────────────────

  const getTRServiceStatus = async (): Promise<TrunkRecorderServiceStatus> => {
    try {
      return await $fetch<TrunkRecorderServiceStatus>('/api/admin/trunk-recorder/service', {
        headers: authHeader(),
      })
    } catch {
      return { running: false, mode: 'native', message: 'unavailable' }
    }
  }

  const trServiceAction = async (action: 'start' | 'stop' | 'restart', binaryPath?: string, configPath?: string): Promise<TrunkRecorderServiceResult> => {
    try {
      return await $fetch<TrunkRecorderServiceResult>('/api/admin/trunk-recorder/service/action', {
        method: 'POST',
        headers: authHeader(),
        body: { action, binaryPath: binaryPath ?? '', configPath: configPath ?? '' },
      })
    } catch (err) {
      handleError(err, `trunk-recorder ${action}`)
      return { success: false, message: String(err) }
    }
  }

  const getTRServiceLogs = async (): Promise<string[]> => {
    try {
      return await $fetch<string[]>('/api/admin/trunk-recorder/service/logs', {
        headers: authHeader(),
      })
    } catch {
      return []
    }
  }

  // ── Bridge status ──────────────────────────────────────────────────────────

  const getBridgeStatus = async (): Promise<BridgeStatus> => {
    try {
      return await $fetch<BridgeStatus>('/api/admin/bridge/status', {
        headers: authHeader(),
      })
    } catch {
      return { running: false, channelCount: 0, mode: 'sdrangel' }
    }
  }

  // ── Dongles ────────────────────────────────────────────────────────────────

  const getDongles = async (): Promise<RTLDongle[]> => {
    try {
      return await $fetch<RTLDongle[]>('/api/admin/dongles', {
        headers: authHeader(),
      })
    } catch {
      return []
    }
  }

  // ── Trunk-recorder config gen ──────────────────────────────────────────────

  const generateTrunkRecorderConfig = async (body: {
    systemRef: number
    controlChannels: number[]
    sources?: unknown[]
    apiKey?: string
    systemType?: string
    uploadURL?: string
    configPath?: string
  }): Promise<{ config: unknown; saveMessage?: string } | null> => {
    try {
      return await $fetch('/api/admin/trunk-recorder/config', {
        method: 'POST',
        headers: authHeader(),
        body,
      })
    } catch (err) {
      handleError(err, 'Generate trunk-recorder config')
      return null
    }
  }

  // ── Users ──────────────────────────────────────────────────────────────────

  const addUser = async (username: string, password: string): Promise<boolean> => {
    try {
      await $fetch('/api/admin/user-add', {
        method: 'POST',
        headers: authHeader(),
        body: { username, password },
      })
      return true
    } catch (err) {
      handleError(err, 'Add user')
      return false
    }
  }

  const removeUser = async (username: string): Promise<boolean> => {
    try {
      await $fetch('/api/admin/user-remove', {
        method: 'POST',
        headers: authHeader(),
        body: { username },
      })
      return true
    } catch (err) {
      handleError(err, 'Remove user')
      return false
    }
  }

  return {
    token: readonly(_token),
    isLoggedIn,
    login,
    logout,
    changePassword,
    getConfig,
    saveConfig,
    getLogs,
    getAlerts,
    importChirp,
    importRRCsv,
    importTrsCsv,
    importFRS,
    importGMRS,
    getRRStates,
    getRRCounties,
    importRRCounty,
    getSDRangelStatus,
    getSDRangelServiceStatus,
    sdrangelServiceAction,
    getSDRangelServiceLogs,
    provisionSDRangel,
    getTRServiceStatus,
    trServiceAction,
    getTRServiceLogs,
    getBridgeStatus,
    getDongles,
    generateTrunkRecorderConfig,
    addUser,
    removeUser,
  }
}
