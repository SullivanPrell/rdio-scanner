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
  // When set (and the dongle is assigned to SDRangel), provisioning drives it with
  // a single Frequency Scanner over its Scan channels instead of a UDPSink each.
  scanEnabled?: boolean
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
  squelchDb: number
  sampleRate: number
  systemRef: number
  talkgroupRef: number
  udpPort: number
  // Include this channel in its device set's Frequency Scanner (effective only when
  // the device set is scanner-enabled). User-set.
  scan?: boolean
  // Scanner detection threshold override (dB) for this frequency only;
  // 0 = auto (group squelch + 10 dB). Detection sits above the capture squelch so
  // ambient carriers idling a few dB over the noise floor don't park the scanner.
  scanThresholdDb?: number
  // SDRangel channel index of the FreqScanner driving this scan channel — set by
  // provisioning. Carried through saves so scan mode survives a config round-trip;
  // not user-editable.
  scannerChannelIndex?: number
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

export interface SDRangelProvisionResult {
  success: boolean
  messages: string[]
}

// Async provision job status, polled while a provision runs in the background.
export interface SDRangelProvisionStatus {
  running: boolean
  done: boolean
  success: boolean
  cancelled?: boolean
  messages: string[]
  startedAt?: number
  finishedAt?: number
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

export interface SDRangelScannerStatus {
  deviceSetIndex: number
  channelIndex: number
  scanState: number // 0 idle, 2 scanning, 3 parked on a transmission
  activeFreqHz?: number
  frequencies?: Array<{ frequencyHz: number; powerDb: number; label?: string }>
}

export interface SDRangelConnectStatus {
  connected: boolean
  version?: string
  os?: string
  deviceSets?: Array<{
    index: number
    hwType: string
    // null when SDRangel created the device set but has no channels on it yet (a Go
    // nil slice marshals to JSON null) — callers must guard before .length/.map.
    channels: Array<{ index: number; idText: string; title: string }> | null
  }>
  scanners?: SDRangelScannerStatus[]
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
    // duration: 0 keeps error toasts open until dismissed (reka-ui disables the
    // auto-close timer for duration <= 0) so a failure can't scroll past unseen.
    toast.add({ title: `${context} failed`, description: msg, color: 'error', duration: 0 })
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

  // A cleared numeric input leaves '' in the model (v-model.number falls back to
  // the raw string when it can't parse), and one non-numeric field makes the
  // server reject the whole bridge channel list — coerce before sending.
  const num = (v: unknown): number => {
    const n = Number(v)
    return Number.isFinite(n) ? n : 0
  }

  const sanitizeBridge = (bridge: BridgeConfig): BridgeConfig => ({
    ...bridge,
    port: num(bridge.port),
    channels: bridge.channels.map(ch => ({
      ...ch,
      channelIndex: num(ch.channelIndex),
      deviceSetIndex: num(ch.deviceSetIndex),
      frequencyHz: num(ch.frequencyHz),
      squelchDb: num(ch.squelchDb),
      sampleRate: num(ch.sampleRate),
      systemRef: num(ch.systemRef),
      talkgroupRef: num(ch.talkgroupRef),
      udpPort: num(ch.udpPort),
      scanThresholdDb: num(ch.scanThresholdDb),
      scannerChannelIndex: num(ch.scannerChannelIndex),
    })),
  })

  const saveConfig = async (cfg: Partial<AdminConfig>): Promise<boolean> => {
    try {
      await $fetch('/api/admin/config', {
        method: 'PUT',
        headers: authHeader(),
        body: cfg.bridge ? { ...cfg, bridge: sanitizeBridge(cfg.bridge) } : cfg,
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

  // Kick off an async provision. Returns the initial job status (running) — the
  // provision then runs server-side; poll getSDRangelProvisionStatus for progress.
  const provisionSDRangel = async (body: unknown): Promise<SDRangelProvisionStatus | null> => {
    try {
      return await $fetch<SDRangelProvisionStatus>('/api/admin/sdrangel/provision', {
        method: 'POST',
        headers: authHeader(),
        body,
      })
    } catch (err) {
      // 409 = a provision is already running; just watch the in-flight one.
      const status = (err as { statusCode?: number; response?: { status?: number } })
      if (status?.statusCode === 409 || status?.response?.status === 409) {
        return { running: true, done: false, success: false, messages: [] }
      }
      handleError(err, 'SDRangel provision')
      return null
    }
  }

  const getSDRangelProvisionStatus = async (): Promise<SDRangelProvisionStatus | null> => {
    try {
      return await $fetch<SDRangelProvisionStatus>('/api/admin/sdrangel/provision/status', {
        headers: authHeader(),
      })
    } catch (err) {
      handleError(err, 'SDRangel provision status')
      return null
    }
  }

  // Cancel an in-flight provision, or wipe a finished/idle job's status so the panel
  // clears and a corrected provision can start. Returns the post-cancel status (or
  // null on transport error). `cancelled` is true when a running job was aborted.
  const cancelSDRangelProvision = async (): Promise<{ cancelled: boolean; status: SDRangelProvisionStatus } | null> => {
    try {
      return await $fetch<{ cancelled: boolean; status: SDRangelProvisionStatus }>('/api/admin/sdrangel/provision/cancel', {
        method: 'POST',
        headers: authHeader(),
      })
    } catch (err) {
      handleError(err, 'SDRangel provision cancel')
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
    getSDRangelProvisionStatus,
    cancelSDRangelProvision,
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
