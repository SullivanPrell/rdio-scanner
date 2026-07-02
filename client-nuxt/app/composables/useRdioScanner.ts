export interface RdioCall {
  id: number
  audio: number[]
  audioName: string
  audioType: string
  dateTime: string
  duration?: number
  frequencies: Array<{ freq: number; pos: number; error?: number; spike?: number }>
  patches?: number[]
  sources: Array<{ src: number; pos: number; tag?: string }>
  system: number
  systemLabel: string
  talkgroup: number
  talkgroupGroup: string
  talkgroupLabel: string
  talkgroupName: string
  talkgroupTag: string
  talkgroupType: string
}

export interface RdioTalkgroup {
  id: number
  label: string
  name: string
  tag: string
  group: string
  type: string
  led?: string
  alert?: string
  delay?: number
  frequency?: number
}

export interface RdioSystem {
  id: number
  label: string
  type: string
  led?: string
  talkgroups: RdioTalkgroup[]
  units: Array<{ id: number; label: string }>
  sites?: Array<{ id: number; label: string }>
}

export interface RdioConfig {
  branding?: string
  dimmerDelay: number | false
  email?: string
  groups: Record<string, number[]>
  playbackGoesLive: boolean
  showListenersCount: boolean
  systems: RdioSystem[]
  tags: Record<string, number[]>
  time12hFormat: boolean
}

export interface RdioSearchOptions {
  date?: string
  system?: number
  talkgroup?: number
  group?: string
  tag?: string
  sort?: number
  limit?: number
  offset?: number
}

export interface RdioListResult {
  count: number
  calls: Array<{
    id: number
    dateTime: string
    system: number
    talkgroup: number
    talkgroupLabel: string
    talkgroupName: string
    duration?: number
  }>
}

export type RdioLivefeedMap = Record<number, Record<number, boolean>>

enum WsCmd {
  Call = 'CAL',
  Config = 'CFG',
  Expired = 'XPR',
  ListCall = 'LCL',
  ListenersCount = 'LSC',
  LivefeedMap = 'LFM',
  Max = 'MAX',
  Pin = 'PIN',
  Version = 'VER',
}

const RECONNECT_DELAY = 3000
const LFM_STORAGE_KEY = 'rdio-scanner-lfm'
const PIN_STORAGE_KEY = 'rdio-scanner-pin'
const HISTORY_MAX = 50
// Queued calls retain their full audio bytes for replay, so an unbounded queue
// (paused live feed, or a long call playing) pins hundreds of KB per call. Cap
// it and drop the oldest overflow.
const QUEUE_MAX = 100

export const useRdioScanner = () => {
  const config          = useState<RdioConfig | null>('rs:config',      () => null)
  const currentCall     = useState<RdioCall | null>  ('rs:currentCall', () => null)
  const callQueue       = useState<RdioCall[]>        ('rs:queue',       () => [])
  const callHistory     = useState<RdioCall[]>        ('rs:history',     () => [])
  const livefeedMap     = useState<RdioLivefeedMap>   ('rs:lfm',         () => ({}))
  const isPlaying       = useState('rs:playing',      () => false)
  const isPaused        = useState('rs:paused',       () => false)
  const livefeedPaused  = useState('rs:lfPaused',     () => false)
  const holdSys         = useState('rs:holdSys',      () => false)
  const holdTg          = useState('rs:holdTg',       () => false)
  const heldSystem      = useState<number | null>('rs:heldSystem',    () => null)
  const heldTalkgroup   = useState<number | null>('rs:heldTalkgroup', () => null)
  const listenersCount  = useState('rs:listeners',    () => 0)
  const serverVersion   = useState('rs:version',      () => '')
  const pinRequired     = useState('rs:pinRequired',  () => false)
  const maxReached      = useState('rs:maxReached',   () => false)
  const connected       = useState('rs:connected',    () => false)
  const playbackProgress = useState('rs:progress',    () => 0)
  const listResult      = useState<RdioListResult | null>('rs:list', () => null)
  const clock           = useState('rs:clock',        () => new Date().toISOString())
  const playbackMode    = useState('rs:playbackMode', () => false)

  let ws: WebSocket | null = null
  let audioCtx: AudioContext | null = null
  let audioSource: AudioBufferSourceNode | null = null
  let audioStartTime = NaN
  let progressTimer: ReturnType<typeof setInterval> | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let clockTimer: ReturnType<typeof setInterval> | null = null
  // Bumped on every playCall/stopAudio so an out-of-order decodeAudioData
  // resolution from a superseded call can't hijack the active playback.
  let playGen = 0

  const getAudioCtx = () => {
    if (!audioCtx) {
      audioCtx = new (window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext)()
    }
    return audioCtx
  }

  const livefeedOnline  = computed(() => Object.keys(livefeedMap.value).length > 0)
  const livefeedOffline = computed(() => Object.keys(livefeedMap.value).length === 0)

  const currentFreq = computed(() => {
    if (!currentCall.value?.frequencies?.length) return 0
    return [...currentCall.value.frequencies].sort((a, b) => b.pos - a.pos)[0]?.freq ?? 0
  })
  const currentError = computed(() =>
    currentCall.value?.frequencies?.reduce((s, f) => s + (f.error ?? 0), 0) ?? 0)
  const currentSpike = computed(() =>
    currentCall.value?.frequencies?.reduce((s, f) => s + (f.spike ?? 0), 0) ?? 0)
  const currentUnit = computed(() => currentCall.value?.sources?.[0]?.src ?? 0)
  const talkgroupLed = computed(() => {
    const call = currentCall.value
    if (!call) return null
    return config.value?.systems
      .find(s => s.id === call.system)
      ?.talkgroups.find(tg => tg.id === call.talkgroup)
      ?.led ?? null
  })

  const send = (cmd: WsCmd, ...args: unknown[]) => {
    if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify([cmd, ...args]))
  }

  const parseMessage = (raw: string) => {
    let msg: unknown[]
    try { msg = JSON.parse(raw) } catch { return }
    if (!Array.isArray(msg) || !msg.length) return
    const [cmd, ...rest] = msg as [string, ...unknown[]]
    switch (cmd) {
      case WsCmd.Config:         handleConfig(rest[0] as RdioConfig); break
      case WsCmd.Call:           handleCall(rest[0] as RdioCall, rest[1] as string); break
      case WsCmd.Expired:        handleAccessExpired(); break
      case WsCmd.ListCall:       listResult.value = normalizeListResult(rest[0]); break
      case WsCmd.ListenersCount: listenersCount.value = rest[0] as number; break
      // The server replies to our LFM with a boolean ack ("livefeed active?"),
      // NOT the map — the client owns the livefeed map. Only adopt an actual map
      // object; otherwise the boolean ack clobbers livefeedMap with `true`, which
      // unchecks every talkgroup and breaks all selection.
      case WsCmd.LivefeedMap:    if (rest[0] && typeof rest[0] === 'object') livefeedMap.value = rest[0] as RdioLivefeedMap; break
      case WsCmd.Max:            maxReached.value = true; break
      // The server sends a bare ["PIN"] frame (no payload) as the auth challenge;
      // any PIN frame means a code is required. Cleared once CFG arrives.
      case WsCmd.Pin:            pinRequired.value = true; break
      case WsCmd.Version:        serverVersion.value = String(rest[0]); break
    }
  }

  const handleConfig = (cfg: RdioConfig) => {
    config.value = cfg
    pinRequired.value = false
    maxReached.value  = false
    if (import.meta.client) {
      const stored = localStorage.getItem(LFM_STORAGE_KEY)
      if (stored) { try { livefeedMap.value = JSON.parse(stored) } catch { /**/ } }
      send(WsCmd.LivefeedMap, livefeedMap.value)
    }
  }

  // The server marshals calls minimally: `audio` is a Node Buffer ({data,type}),
  // system/talkgroup are refs only (no labels), and frequencies use
  // errorCount/spikeCount. Normalize to the flat shape the UI renders, unwrapping
  // the audio bytes and enriching labels from the loaded config (config system /
  // talkgroup `id` == the call's system / talkgroup ref).
  const normalizeCall = (raw: RdioCall): RdioCall => {
    const audioField = raw.audio as unknown as number[] | { data: number[] } | undefined
    const audio = Array.isArray(audioField) ? audioField : (audioField?.data ?? [])
    const sys = config.value?.systems.find(s => s.id === raw.system)
    const tg = sys?.talkgroups.find(t => t.id === raw.talkgroup)
    const freqs = (raw.frequencies ?? []) as Array<Record<string, number>>
    return {
      ...raw,
      audio,
      systemLabel:    raw.systemLabel    || sys?.label || '',
      talkgroupLabel: raw.talkgroupLabel || tg?.label || String(raw.talkgroup ?? ''),
      talkgroupName:  raw.talkgroupName  || tg?.name  || '',
      talkgroupTag:   raw.talkgroupTag   || tg?.tag   || '',
      talkgroupType:  raw.talkgroupType  || tg?.type  || '',
      talkgroupGroup: raw.talkgroupGroup || tg?.group || '',
      frequencies: freqs.map(f => ({
        freq: f.freq,
        pos: f.pos,
        error: f.error ?? f.errorCount ?? 0,
        spike: f.spike ?? f.spikeCount ?? 0,
      })),
    }
  }

  const handleCall = (raw: RdioCall, flag: string) => {
    const call = normalizeCall(raw)
    if (!call.audio.length) return
    // History is display-only (never replayed); drop the audio bytes so 50 calls
    // don't pin tens of MB of reactive state per tab.
    callHistory.value = [{ ...call, audio: [] }, ...callHistory.value].slice(0, HISTORY_MAX)
    // An explicitly requested call (search result / playback) carries a flag —
    // play it immediately rather than subjecting it to live-feed holds/queueing.
    if (flag) {
      playbackMode.value = true
      stopAudio(false)
      isPaused.value = false
      callQueue.value = []
      playCall(call)
      return
    }
    // Holds compare against a snapshot captured at toggle time, not currentCall —
    // currentCall goes null between calls, which would let the hold drift.
    if (holdSys.value && heldSystem.value    != null && call.system    !== heldSystem.value)    return
    if (holdTg.value  && heldTalkgroup.value != null && call.talkgroup !== heldTalkgroup.value) return
    if (livefeedPaused.value) { callQueue.value = [...callQueue.value, call].slice(-QUEUE_MAX); return }
    if (isPlaying.value || isPaused.value) {
      callQueue.value = [...callQueue.value, call].slice(-QUEUE_MAX)
    } else {
      playCall(call)
    }
  }

  // The server's call-search reply (LCL) is { count, results:[{id,system,talkgroup,
  // dateTime}], ... } — the items are refs with no labels, under `results` not
  // `calls`. Map it to the flat shape SearchPanel renders, enriching labels from
  // config. (Without this, listResult.calls is undefined and the panel throws.)
  const normalizeListResult = (raw: unknown): RdioListResult => {
    const r = (raw ?? {}) as { count?: number; results?: unknown[]; calls?: unknown[] }
    const items = (r.results ?? r.calls ?? []) as Array<Record<string, unknown>>
    return {
      count: r.count ?? items.length,
      calls: items.map((c) => {
        const system = c.system as number
        const talkgroup = c.talkgroup as number
        const sys = config.value?.systems.find(s => s.id === system)
        const tg = sys?.talkgroups.find(t => t.id === talkgroup)
        return {
          id: c.id as number,
          dateTime: c.dateTime as string,
          system,
          talkgroup,
          talkgroupLabel: (c.talkgroupLabel as string) || tg?.label || String(talkgroup ?? ''),
          talkgroupName: (c.talkgroupName as string) || tg?.name || '',
          duration: c.duration as number | undefined,
        }
      }),
    }
  }

  // End of the current call: stop audio and advance to the next queued call.
  const advanceQueue = () => {
    stopAudio()
    currentCall.value  = null
    isPlaying.value    = false
    playbackMode.value = false
    if (callQueue.value.length) {
      const [next, ...rest] = callQueue.value
      callQueue.value = rest
      playCall(next)
    }
  }

  // Server XPR: the access code expired. Distinct from end-of-call — clear the
  // stale PIN and re-open the PIN challenge rather than silently draining.
  const handleAccessExpired = () => {
    stopAudio()
    currentCall.value  = null
    isPlaying.value    = false
    playbackMode.value = false
    callQueue.value    = []
    if (import.meta.client) localStorage.removeItem(PIN_STORAGE_KEY)
    pinRequired.value = true
  }

  const playCall = async (call: RdioCall) => {
    const gen = ++playGen
    currentCall.value      = call
    isPlaying.value        = true
    playbackProgress.value = 0
    try {
      const ctx = getAudioCtx()
      if (ctx.state === 'suspended') await ctx.resume()
      if (gen !== playGen) return
      const bytes = new Uint8Array(call.audio)
      const audioBuffer = await ctx.decodeAudioData(bytes.buffer)
      if (gen !== playGen) return
      stopAudio(false)
      audioSource = ctx.createBufferSource()
      audioSource.buffer = audioBuffer
      audioSource.connect(ctx.destination)
      audioStartTime = ctx.currentTime
      audioSource.onended = () => { if (isPlaying.value) advanceQueue() }
      audioSource.start(0)
      const duration = audioBuffer.duration
      if (progressTimer) clearInterval(progressTimer)
      progressTimer = setInterval(() => {
        const elapsed = ctx.currentTime - audioStartTime
        playbackProgress.value = Math.min(100, (elapsed / duration) * 100)
        if (elapsed >= duration && progressTimer) clearInterval(progressTimer)
      }, 100)
    } catch (err) {
      if (gen !== playGen) return
      console.error('Audio decode error:', err)
      advanceQueue()
    }
  }

  const stopAudio = (clearState = true) => {
    playGen++
    if (progressTimer) { clearInterval(progressTimer); progressTimer = null }
    if (audioSource) {
      try { audioSource.onended = null; audioSource.stop(); audioSource.disconnect() } catch { /**/ }
      audioSource = null
    }
    if (clearState) { isPlaying.value = false; playbackProgress.value = 0 }
  }

  const connect = () => {
    if (ws) return
    const url = typeof window !== 'undefined' ? window.location.href.replace(/^http/, 'ws') : ''
    if (!url) return
    ws = new WebSocket(url)
    ws.onopen = () => {
      connected.value = true
      maxReached.value = false
      send(WsCmd.Version)
      send(WsCmd.Config)
      if (import.meta.client) {
        const pin = localStorage.getItem(PIN_STORAGE_KEY)
        if (pin) send(WsCmd.Pin, pin)
      }
    }
    ws.onclose   = () => { connected.value = false; ws = null; reconnectTimer = setTimeout(connect, RECONNECT_DELAY) }
    ws.onerror   = () => { ws?.close() }
    ws.onmessage = (ev) => parseMessage(ev.data)
    if (import.meta.client) {
      clock.value = new Date().toISOString()
      // connect() re-runs on every reconnect; clear any prior clock interval
      // first so reconnects don't orphan a 1 Hz timer each (they accumulate and
      // eventually freeze the tab with redundant re-renders).
      if (clockTimer) clearInterval(clockTimer)
      clockTimer  = setInterval(() => { clock.value = new Date().toISOString() }, 1000)
    }
  }

  const disconnect = () => {
    if (reconnectTimer) clearTimeout(reconnectTimer)
    if (clockTimer) { clearInterval(clockTimer); clockTimer = null }
    if (ws) { ws.onclose = null; ws.close(); ws = null }
    stopAudio()
    // Browsers cap concurrent AudioContexts (~6); each scanner mount creates a
    // fresh closure, so without closing it here every visit leaks one.
    if (audioCtx) { audioCtx.close().catch(() => {}); audioCtx = null }
    connected.value = false
  }

  const submitPin = (pin: string) => {
    if (import.meta.client) localStorage.setItem(PIN_STORAGE_KEY, btoa(pin))
    send(WsCmd.Pin, btoa(pin))
  }

  const setLivefeedMap = (lfm: RdioLivefeedMap) => {
    livefeedMap.value = lfm
    if (import.meta.client) localStorage.setItem(LFM_STORAGE_KEY, JSON.stringify(lfm))
    send(WsCmd.LivefeedMap, lfm)
  }

  const toggleLivefeed = () => {
    if (livefeedOffline.value) {
      const lfm: RdioLivefeedMap = {}
      config.value?.systems.forEach(sys => {
        lfm[sys.id] = {}
        sys.talkgroups.forEach(tg => { lfm[sys.id][tg.id] = true })
      })
      setLivefeedMap(lfm)
    } else {
      setLivefeedMap({})
    }
  }

  const toggleSystem = (systemRef: number, enabled: boolean) => {
    const lfm = { ...livefeedMap.value }
    const sys = config.value?.systems.find(s => s.id === systemRef)
    if (!sys) return
    if (enabled) { lfm[systemRef] = {}; sys.talkgroups.forEach(tg => { lfm[systemRef][tg.id] = true }) }
    else { delete lfm[systemRef] }
    setLivefeedMap(lfm)
  }

  const toggleTalkgroup = (systemRef: number, talkgroupRef: number, enabled: boolean) => {
    const lfm = { ...livefeedMap.value }
    if (!lfm[systemRef]) lfm[systemRef] = {}
    if (enabled) { lfm[systemRef][talkgroupRef] = true }
    else { delete lfm[systemRef][talkgroupRef]; if (!Object.keys(lfm[systemRef]).length) delete lfm[systemRef] }
    setLivefeedMap(lfm)
  }

  const toggleHoldSystem = () => {
    holdSys.value = !holdSys.value
    heldSystem.value = holdSys.value ? (currentCall.value?.system ?? null) : null
  }
  const toggleHoldTalkgroup = () => {
    holdTg.value = !holdTg.value
    heldTalkgroup.value = holdTg.value ? (currentCall.value?.talkgroup ?? null) : null
  }
  const toggleLivefeedPause = () => {
    livefeedPaused.value = !livefeedPaused.value
    if (!livefeedPaused.value && !isPlaying.value && !isPaused.value && callQueue.value.length) {
      const [next, ...rest] = callQueue.value
      callQueue.value = rest
      playCall(next)
    }
  }
  const pause  = () => { if (!isPlaying.value) return; stopAudio(false); isPaused.value = true; isPlaying.value = false }
  const resume = () => { if (!isPaused.value || !currentCall.value) return; isPaused.value = false; playCall(currentCall.value) }
  const skip   = () => { advanceQueue() }
  const replay = () => { if (currentCall.value) playCall(currentCall.value) }
  const avoid  = () => { /* TODO: server-side avoid */ }

  const requestCall = (id: number, download = false) => {
    playbackMode.value = true
    send(WsCmd.Call, `${id}`, download ? 'd' : 'p')
  }
  const searchCalls = (opts: RdioSearchOptions) => { send(WsCmd.ListCall, opts) }

  const isSystemEnabled    = (systemRef: number) => systemRef in livefeedMap.value
  const isTalkgroupEnabled = (systemRef: number, talkgroupRef: number) => !!livefeedMap.value[systemRef]?.[talkgroupRef]
  const getSystemLiveStatus = (systemRef: number): 'on' | 'off' | 'partial' => {
    const sys = config.value?.systems.find(s => s.id === systemRef)
    if (!sys) return 'off'
    const lfm = livefeedMap.value[systemRef]
    if (!lfm) return 'off'
    const active = sys.talkgroups.filter(tg => lfm[tg.id]).length
    if (active === 0) return 'off'
    if (active === sys.talkgroups.length) return 'on'
    return 'partial'
  }

  return {
    config, currentCall, callQueue, callHistory, livefeedMap,
    isPlaying, isPaused, livefeedPaused, holdSys, holdTg,
    livefeedOnline, livefeedOffline,
    listenersCount, serverVersion, pinRequired, maxReached,
    connected, playbackProgress, listResult, clock, playbackMode,
    currentFreq, currentError, currentSpike, currentUnit, talkgroupLed,
    connect, disconnect, submitPin,
    setLivefeedMap, toggleLivefeed, toggleSystem, toggleTalkgroup,
    toggleHoldSystem, toggleHoldTalkgroup, toggleLivefeedPause,
    pause, resume, skip, replay, avoid, requestCall, searchCalls,
    isSystemEnabled, isTalkgroupEnabled, getSystemLiveStatus,
  }
}
