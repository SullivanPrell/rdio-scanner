<script setup lang="ts">
import type {
  BridgeConfig,
  BridgeChannel,
  AdminSystem,
  RTLDongle,
  SDRDeviceAssignment,
  SDRangelServiceStatus,
  SDRangelConnectStatus,
  TrunkRecorderServiceStatus,
  BridgeStatus,
} from '~/composables/useAdmin'

const props = defineProps<{ modelValue: BridgeConfig; systems: AdminSystem[] }>()
const emit = defineEmits<{ 'update:modelValue': [BridgeConfig] }>()
const bridge = ref<BridgeConfig>({
  ...props.modelValue,
  channels: props.modelValue?.channels ?? [],
  sdrDeviceAssignments: props.modelValue?.sdrDeviceAssignments ?? [],
})

watch(bridge, val => emit('update:modelValue', val), { deep: true })

const admin = useAdmin()
const toast = useToast()

// ── Sub-tab ──────────────────────────────────────────────────────────────────
const subTab = ref<'sdrangel' | 'trunk-recorder' | 'devices' | 'channels'>('sdrangel')

// ── SDRangel service state ────────────────────────────────────────────────────
const sdrangelSvc = ref<SDRangelServiceStatus>({ running: false, mode: 'native' })
const sdrangelLogs = ref<string[]>([])
const sdrangelActioning = ref(false)
const sdrangelLogsEl = ref<HTMLElement | null>(null)
// REST view of what SDRangel currently has provisioned (device sets + channels).
const sdrangelStatus = ref<SDRangelConnectStatus>({ connected: false })
const provisionedChannelCount = computed(() =>
  (sdrangelStatus.value.deviceSets ?? []).reduce((n, ds) => n + ds.channels.length, 0),
)
// SDRangel's device-set listing doesn't carry a channel's frequency or UDP port, so
// join each provisioned channel back to the local bridge config (by device-set +
// channel index, falling back to a label/title match) to surface freq + port.
const provisionedDeviceSets = computed(() => {
  const chans = bridge.value.channels ?? []
  return (sdrangelStatus.value.deviceSets ?? []).map(ds => ({
    index: ds.index,
    hwType: ds.hwType,
    channels: ds.channels.map(ch => {
      const m = chans.find(c => c.deviceSetIndex === ds.index && c.channelIndex === ch.index)
        ?? chans.find(c => !!c.label && c.label === ch.title)
      return {
        index: ch.index,
        label: ch.title || ch.idText || `ch ${ch.index}`,
        freqHz: m?.frequencyHz,
        udpPort: m?.udpPort,
      }
    }),
  }))
})

function formatMHz(hz?: number): string {
  if (!hz) return ''
  return `${(hz / 1e6).toFixed(4).replace(/\.?0+$/, '')} MHz`
}

// ── Trunk-recorder service state ──────────────────────────────────────────────
const trSvc = ref<TrunkRecorderServiceStatus>({ running: false, mode: 'native' })
const trLogs = ref<string[]>([])
const trActioning = ref(false)
const trLogsEl = ref<HTMLElement | null>(null)

// ── Bridge relay state ────────────────────────────────────────────────────────
const bridgeSvc = ref<BridgeStatus>({ running: false, channelCount: 0, mode: 'sdrangel' })

// ── SDR Devices ───────────────────────────────────────────────────────────────
const dongles = ref<RTLDongle[]>([])
const detectingDongles = ref(false)

// ── SDRangel provisioning ─────────────────────────────────────────────────────
const provisioning = ref(false)
// Per-step output from the last provision() call. The server returns these in
// result.messages; the Live Logs ring buffer only captures stdout from an
// sdrangelsrv that rdio-scanner spawned itself (empty for an adopted/external
// instance), so this is the real diagnostic of what provisioning actually did.
const provisionMessages = ref<string[]>([])

// ── Trunk-recorder config generation ─────────────────────────────────────────
const trGenSystemRef = ref<number>(0)
const trGenControlChannels = ref('')
const trGenSystemType = ref('P25')
const trGenerating = ref(false)
const trGenMessage = ref('')
const trFrequencies = ref<number[]>([]) // all site frequencies (Hz) from an imported sites CSV
const trSitesFileName = ref('')
// trDeviceString builds the gr-osmosdr device arg that pins one trunk-recorder
// dongle. A non-numeric serial is passed straight through (`rtl=<serial>`) —
// gr-osmosdr resolves it via librtlsdr at runtime, so it survives re-enumeration,
// exactly how SDRangel pins its dongles by serial. A numeric or empty serial can't
// be used that way (gr-osmosdr reads a numeric `rtl=` value as a device index, not a
// serial), so we resolve the serial to its CURRENT rtl_test index and pin
// `rtl=<index>` instead — unambiguous, though it needs regenerating if dongles are
// re-plugged. Pinning both apps by serial is what keeps trunk-recorder and SDRangel
// from ever fighting over the same physical dongle.
function trDeviceString(a: SDRDeviceAssignment): string {
  const serial = (a.serialNumber || '').trim()
  if (serial && !/^\d+$/.test(serial)) return `rtl=${serial}`
  const live = serial ? dongles.value.find(d => d.serialNumber === serial) : undefined
  return `rtl=${live ? live.index : a.index}`
}

// Dongles the operator assigned to trunk-recorder, as resolved gr-osmosdr device
// strings (assignment order). One device string per assigned dongle.
const trDongleDevices = computed(() =>
  bridge.value.sdrDeviceAssignments
    .filter(a => a.assignTo === 'trunk-recorder')
    .map(trDeviceString))

// Surface dongle-identity problems that make assignment unreliable: an assigned
// dongle with no serial, or two assigned dongles sharing a serial, can't be pinned
// distinctly — both SDRangel and trunk-recorder address dongles by serial, so a
// missing/duplicate serial risks the two apps grabbing the same physical device.
const sdrAssignmentWarnings = computed(() => {
  const assigned = bridge.value.sdrDeviceAssignments.filter(a => a.assignTo)
  const warnings: string[] = []
  const counts = new Map<string, number>()
  let missing = 0
  for (const a of assigned) {
    const s = (a.serialNumber || '').trim()
    if (!s) { missing++; continue }
    counts.set(s, (counts.get(s) ?? 0) + 1)
  }
  if (missing) warnings.push(`${missing} assigned dongle(s) have no serial — set a unique serial with rtl_eeprom so each can be pinned to one app.`)
  const dups = [...counts.entries()].filter(([, n]) => n > 1).map(([s]) => s)
  if (dups.length) warnings.push(`Duplicate serial(s) ${dups.join(', ')} on assigned dongles — give each dongle a unique serial (rtl_eeprom), or SDRangel and trunk-recorder may grab the same one.`)
  return warnings
})

// Dongles assigned to SDRangel, in assignment order. This order DEFINES the device
// sets: device-set index i is bound to the i-th SDRangel dongle (its serial pins the
// physical dongle, its scanEnabled makes that set a Frequency Scanner). A channel's
// deviceSetIndex therefore selects which assigned dongle handles it.
const sdrangelAssignments = computed(() =>
  bridge.value.sdrDeviceAssignments.filter(a => a.assignTo === 'sdrangel'))

// Device-set indices whose dongle has Scanning enabled — the only sets a Scan
// channel may be provisioned on.
const scannerSetIndices = computed(() =>
  sdrangelAssignments.value.map((a, i) => ({ a, i })).filter(x => x.a.scanEnabled).map(x => x.i))

// Options for the per-channel "Dev Set" picker: one entry per SDRangel-assigned
// dongle (so the choice maps to a real physical dongle), plus Unassigned. Without
// this the Dev Set was a free-form integer with no connection to which dongle ran
// it — the reason assignment "didn't seem to work".
const deviceSetOptions = computed(() => {
  const opts = sdrangelAssignments.value.map((a, i) => ({
    label: `Set ${i} · ${a.serialNumber || 'dongle'}${a.scanEnabled ? ' · scanner' : ''}`,
    value: i,
  }))
  opts.push({ label: 'Unassigned', value: -1 })
  return opts
})

// ── Polling ───────────────────────────────────────────────────────────────────
let pollTimer: ReturnType<typeof setInterval> | null = null
let provisionPollTimer: ReturnType<typeof setTimeout> | null = null

async function refreshAll() {
  const [svc, tr, br, sdr] = await Promise.all([
    admin.getSDRangelServiceStatus(),
    admin.getTRServiceStatus(),
    admin.getBridgeStatus(),
    admin.getSDRangelStatus(),
  ])
  sdrangelSvc.value = svc
  trSvc.value = tr
  bridgeSvc.value = br
  sdrangelStatus.value = sdr
}

async function refreshSDRangelLogs() {
  sdrangelLogs.value = await admin.getSDRangelServiceLogs()
  await nextTick()
  if (sdrangelLogsEl.value) sdrangelLogsEl.value.scrollTop = sdrangelLogsEl.value.scrollHeight
}

async function refreshTRLogs() {
  trLogs.value = await admin.getTRServiceLogs()
  await nextTick()
  if (trLogsEl.value) trLogsEl.value.scrollTop = trLogsEl.value.scrollHeight
}

onMounted(async () => {
  await refreshAll()
  dongles.value = await admin.getDongles()
  pollTimer = setInterval(refreshAll, 8000)
  // A provision started earlier may still be running server-side (it's async and
  // survives a tab reload) — resume showing its progress.
  await resumeProvisionIfRunning()
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (provisionPollTimer) clearTimeout(provisionPollTimer)
})

watch(subTab, async (tab) => {
  if (tab === 'sdrangel') {
    await refreshSDRangelLogs()
    await resumeProvisionIfRunning()
  }
  if (tab === 'trunk-recorder') await refreshTRLogs()
})

// ── SDRangel actions ──────────────────────────────────────────────────────────
async function sdrangelAction(action: 'start' | 'stop' | 'restart') {
  sdrangelActioning.value = true
  const result = await admin.sdrangelServiceAction(
    action,
    bridge.value.sdrangelBinaryPath || undefined,
  )
  sdrangelActioning.value = false
  toast.add({
    title: result.success ? `sdrangelsrv ${action}ed` : `Failed to ${action} sdrangelsrv`,
    description: result.message,
    color: result.success ? 'success' : 'error',
  })
  await refreshAll()
  await refreshSDRangelLogs()
}

const SDR_SAMPLE_RATE = 2400000 // ~2.4 MHz usable window per RTL-SDR dongle

// routeScanChannels authoritatively moves every Scan channel onto a scanner-enabled
// device set BEFORE provisioning computes device-set centers — so the scanner set
// actually appears in the payload (the server can't relocate onto a set it never
// receives) and is centered to cover the channel. A scan channel is moved to the
// least-loaded scanner whose existing channels still fit one ~2 MHz window once it's
// added (so the recomputed center covers them all); one that fits no scanner is left
// in place and reported, rather than pinned to a dongle that can't sample it. Mirrors
// the server's routeScanChannelsToScanners so both paths agree.
function routeScanChannels(channels: BridgeChannel[]): { channels: BridgeChannel[]; warnings: string[] } {
  const scannerSets = scannerSetIndices.value
  const warnings: string[] = []
  if (!scannerSets.length) return { channels, warnings }

  const freqsOf = new Map<number, number[]>()
  for (const c of channels) {
    if (c.frequencyHz > 0 && c.deviceSetIndex >= 0) {
      const a = freqsOf.get(c.deviceSetIndex) ?? []
      a.push(c.frequencyHz)
      freqsOf.set(c.deviceSetIndex, a)
    }
  }
  const fits = (set: number, hz: number) => {
    const fs = freqsOf.get(set)
    if (!fs || !fs.length) return true
    return Math.max(...fs, hz) - Math.min(...fs, hz) <= SDR_USABLE_HZ
  }
  const load = new Map<number, number>()
  for (const c of channels) {
    if (c.scan && scannerSets.includes(c.deviceSetIndex)) load.set(c.deviceSetIndex, (load.get(c.deviceSetIndex) ?? 0) + 1)
  }
  const pick = (hz: number) => {
    let target = -1
    for (const s of scannerSets) {
      if (!fits(s, hz)) continue
      if (target < 0 || (load.get(s) ?? 0) < (load.get(target) ?? 0)) target = s
    }
    return target
  }

  const out = channels.map((c) => {
    if (!c.scan || c.frequencyHz <= 0 || scannerSets.includes(c.deviceSetIndex)) return c
    const target = pick(c.frequencyHz)
    if (target < 0) {
      warnings.push(`${c.label || 'channel'} at ${(c.frequencyHz / 1e6).toFixed(4)} MHz fits no scanner dongle's ~2 MHz window — left on its current device set; it won't be scanned.`)
      return c
    }
    load.set(target, (load.get(target) ?? 0) + 1)
    const fs = freqsOf.get(target) ?? []
    fs.push(c.frequencyHz)
    freqsOf.set(target, fs)
    return { ...c, deviceSetIndex: target }
  })
  return { channels: out, warnings }
}

async function provision() {
  provisioning.value = true

  // A Scan channel can only be provisioned behind a Frequency Scanner on a dongle
  // marked Scanning — so if any channel is flagged Scan but no dongle has Scanning
  // enabled, block here with a clear message rather than silently provisioning those
  // channels as un-scanned fixed UDPSinks.
  const scanChannels = bridge.value.channels.filter(c => c.scan && c.frequencyHz > 0)
  if (scanChannels.length && !scannerSetIndices.value.length) {
    provisioning.value = false
    provisionMessages.value = [`provision: aborted — ${scanChannels.length} channel(s) are flagged Scan but no dongle has Scanning enabled. Enable Scanning on an SDRangel dongle (SDR Devices tab), then provision.`]
    toast.add({ title: 'No scanner dongle', description: 'Enable Scanning on an SDRangel dongle before provisioning scan channels.', color: 'error' })
    return
  }

  // Move any Scan channel that's sitting on a plain (non-scanner) device set onto a
  // scanner dongle, so it actually scans and the scanner set ends up in the payload
  // (centered to cover it). Persisted below, so the saved config matches what gets
  // provisioned and the server's identical routing is a no-op confirmation.
  const routed = routeScanChannels(bridge.value.channels)
  if (routed.warnings.length) {
    routed.warnings.forEach(w => toast.add({ title: 'Scan channel not placed', description: w, color: 'warning' }))
  }
  bridge.value = { ...bridge.value, channels: routed.channels }

  // Provisioning reads the SAVED bridge config server-side (the request body's
  // channels are advisory), and scan mode is gated on each channel's saved Scan flag.
  // Persist the current edit buffer first so freshly-ticked Scan boxes actually take
  // effect — otherwise the dongle would be scanner-enabled (that travels live in the
  // request) while every channel's Scan came back false, silently disabling scanning.
  if (!(await admin.saveConfig({ bridge: bridge.value }))) {
    provisioning.value = false
    provisionMessages.value = ['provision: aborted — config save failed (see error toast)']
    return
  }

  // Group channels by SDR device set and centre each dongle on the midpoint of
  // its channels, so one dongle's ~2.4 MHz window covers them all. (Centring on
  // the first channel left channels more than ~1.2 MHz away out of range.)
  const freqsByDevice = new Map<number, number[]>()
  for (const ch of bridge.value.channels) {
    if (!ch.frequencyHz || ch.deviceSetIndex < 0) continue // skip parked/unassigned
    const arr = freqsByDevice.get(ch.deviceSetIndex) ?? []
    arr.push(ch.frequencyHz)
    freqsByDevice.set(ch.deviceSetIndex, arr)
  }

  // Pin each device set to a specific dongle assigned to SDRangel (by serial), so
  // SDRangel uses only its dongles and leaves the trunk-recorder ones alone. Device
  // set index i maps to the i-th SDRangel-assigned dongle: its serial pins the exact
  // physical dongle and its scanEnabled makes the set a Frequency Scanner. This is
  // the SAME ordering the Dev Set picker and Auto-assign use, so the dongle a channel
  // is assigned to is the dongle it actually runs on.
  const assigned = sdrangelAssignments.value

  const deviceSets = [...freqsByDevice.entries()].map(([index, freqs]) => {
    const min = Math.min(...freqs)
    const max = Math.max(...freqs)
    let center = Math.round((min + max) / 2)
    // Nudge the centre off any channel that lands exactly on it — RTL-SDR has a
    // DC spike at the tuned centre frequency that would corrupt that channel.
    if (freqs.includes(center)) center += 100000
    if (max - min > SDR_SAMPLE_RATE * 0.9) {
      toast.add({
        title: `Device set ${index}: channels span ${((max - min) / 1e6).toFixed(2)} MHz`,
        description: `One SDR only covers ~${(SDR_SAMPLE_RATE / 1e6).toFixed(1)} MHz. Spread these across more device sets.`,
        color: 'warning',
      })
    }
    return {
      index,
      hwType: 'RTLSDR',
      sequence: index,
      serial: assigned[index]?.serialNumber ?? '',
      centerFrequencyHz: center,
      sampleRateHz: SDR_SAMPLE_RATE,
      scannerEnabled: assigned[index]?.scanEnabled ?? false,
    }
  })

  const kickoff = await admin.provisionSDRangel({ deviceSets, channels: bridge.value.channels.filter(c => c.deviceSetIndex >= 0) })

  if (!kickoff) {
    // Couldn't even start — handleError already toasted the details.
    provisioning.value = false
    provisionMessages.value = ['provision: could not start — see error toast (is the server reachable?)']
    return
  }

  // Provisioning now runs in the BACKGROUND on the server (minutes long); it keeps
  // going even if this tab closes. Poll the job for live progress until it finishes.
  provisionMessages.value = kickoff.messages?.length
    ? kickoff.messages
    : ['provision: started — running in the background (this can take a few minutes)…']
  if (provisionPollTimer) clearTimeout(provisionPollTimer)
  await pollProvision()
}

// pollProvision pulls the async provision job's status, updates the live message
// panel, and reschedules itself until the job finishes — then toasts the outcome.
async function pollProvision() {
  const status = await admin.getSDRangelProvisionStatus()
  if (!status) {
    provisioning.value = false
    return
  }
  if (status.messages.length) provisionMessages.value = status.messages

  if (!status.done) {
    provisioning.value = true
    provisionPollTimer = setTimeout(pollProvision, 2000)
    return
  }

  // Finished.
  provisioning.value = false
  // A deliberate cancel isn't a failure — report it neutrally and stop, so the
  // operator doesn't get a red "provision failed" for an abort they requested.
  if (status.cancelled) {
    provisionMessages.value = status.messages.length ? status.messages : ['provision: cancelled']
    toast.add({ title: 'Provision cancelled', color: 'neutral' })
    return
  }
  if (!status.messages.length) provisionMessages.value = ['provision: returned no messages']
  // success is only false on early-exit errors; per-channel failures still report
  // success=true, so scan the messages for problems too.
  const problems = status.messages.filter(m => /failed|warning|cannot/i.test(m))
  if (!status.success || problems.length) {
    toast.add({
      title: status.success ? `Provisioned with ${problems.length} problem(s)` : 'SDRangel provision failed',
      description: problems[0] ?? 'See the provision output below.',
      color: status.success ? 'warning' : 'error',
      duration: 0, // persist — a provision problem shouldn't auto-dismiss before it's read
    })
  } else {
    toast.add({ title: 'SDRangel provisioned', color: 'success' })
  }
  // Pull any stdout from a managed sdrangelsrv (e.g. a crash during provisioning).
  await refreshSDRangelLogs()
}

// cancelProvision aborts an in-flight provision, or wipes a finished/idle run's
// output panel — so a mistake (wrong channels marked Scan, wrong dongle) can be
// undone and a corrected provision started right away. A running job is signalled to
// stop at its next safe step server-side; the poller then sees it finish.
const cancelling = ref(false)
async function cancelProvision() {
  cancelling.value = true
  const res = await admin.cancelSDRangelProvision()
  cancelling.value = false
  if (!res) return // transport error already toasted
  if (res.cancelled) {
    // Still running server-side until it reaches a safe boundary — keep polling so
    // the panel updates as it winds down, then re-enables Provision on its own.
    if (res.status.messages.length) provisionMessages.value = res.status.messages
    toast.add({ title: 'Provision cancelling…', description: 'Stopping at the next safe step.', color: 'warning' })
    if (!provisioning.value) {
      provisioning.value = true
      if (provisionPollTimer) clearTimeout(provisionPollTimer)
      void pollProvision()
    }
  } else {
    // Nothing was running: the server cleared the status; clear the local panel too.
    if (provisionPollTimer) clearTimeout(provisionPollTimer)
    provisioning.value = false
    provisionMessages.value = []
    toast.add({ title: 'Provision status cleared', color: 'success' })
  }
}

// resumeProvisionIfRunning re-attaches the live poll to a provision still running
// server-side (e.g. after a tab reload or switching back to the SDRangel sub-tab).
let resuming = false
async function resumeProvisionIfRunning() {
  if (provisioning.value || resuming) return // already polling / resume in flight
  // Hold a synchronous re-entry lock across the await so two near-simultaneous calls
  // (onMounted + watch(subTab)) can't both pass the check and spawn duplicate pollers.
  resuming = true
  try {
    const status = await admin.getSDRangelProvisionStatus()
    if (status?.running && !provisioning.value) {
      if (status.messages.length) provisionMessages.value = status.messages
      provisioning.value = true
      if (provisionPollTimer) clearTimeout(provisionPollTimer)
      void pollProvision()
    }
  } finally {
    resuming = false
  }
}

// ── Trunk-recorder actions ────────────────────────────────────────────────────
async function trAction(action: 'start' | 'stop' | 'restart') {
  trActioning.value = true
  // Paths are managed server-side (default to the install location and the
  // server's base dir), so we don't pass them — nothing for the operator to set.
  const result = await admin.trServiceAction(action)
  trActioning.value = false
  toast.add({
    title: result.success ? `trunk-recorder ${action}ed` : `Failed to ${action} trunk-recorder`,
    description: result.message,
    color: result.success ? 'success' : 'error',
  })
  await refreshAll()
  await refreshTRLogs()
}

// Parse a "MHz or Hz, comma-separated" field into Hz. A value < 1e6 is read as MHz.
function parseFreqField(value: string): number[] {
  return value
    .split(',')
    .map(s => s.trim())
    .filter(Boolean)
    .map(s => {
      const n = parseFloat(s)
      return n < 1e6 ? Math.round(n * 1e6) : Math.round(n)
    })
    .filter(n => n > 0)
}

// Minimal CSV line splitter that respects double-quoted fields.
function parseCsvLine(line: string): string[] {
  const out: string[] = []
  let cur = '', inQuotes = false
  for (let i = 0; i < line.length; i++) {
    const c = line[i]
    if (inQuotes) {
      if (c === '"') { if (line[i + 1] === '"') { cur += '"'; i++ } else inQuotes = false }
      else cur += c
    } else if (c === '"') inQuotes = true
    else if (c === ',') { out.push(cur); cur = '' }
    else cur += c
  }
  out.push(cur)
  return out
}

// Import a RadioReference TRS *sites* CSV. Columns 0-8 are site metadata; every
// column from 9 on is a frequency in MHz, optionally suffixed with 'c' (control
// channel) / 'a' (alternate). Fills the control-channel field and records the full
// frequency list used to plan SDR coverage windows.
async function importSitesCSV(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  const lines = (await file.text()).split(/\r?\n/).filter(l => l.trim())
  const controlHz: number[] = []
  const allHz: number[] = []
  for (const line of lines) {
    const f = parseCsvLine(line)
    if (!f.length || /^rfss$/i.test(f[0].trim())) continue // skip header
    for (let i = 9; i < f.length; i++) {
      const m = f[i].trim().match(/^(\d+(?:\.\d+)?)\s*([a-zA-Z]*)$/)
      if (!m) continue
      const hz = Math.round(parseFloat(m[1]) * 1e6) // sites CSV frequencies are MHz
      if (!hz) continue
      allHz.push(hz)
      if (m[2].toLowerCase().includes('c')) controlHz.push(hz)
    }
  }
  input.value = '' // allow re-uploading the same file
  if (!allHz.length) {
    toast.add({ title: 'No frequencies found in CSV', description: 'Expected a RadioReference TRS sites export.', color: 'error' })
    return
  }
  const uniq = (a: number[]) => [...new Set(a)].sort((x, y) => x - y)
  trFrequencies.value = uniq(allHz)
  const ctrl = uniq(controlHz.length ? controlHz : allHz)
  trGenControlChannels.value = ctrl.map(h => (h / 1e6).toString()).join(', ')
  trSitesFileName.value = file.name
  toast.add({
    title: `Imported ${trFrequencies.value.length} frequencies`,
    description: `${ctrl.length} control channel(s); ${trDongleDevices.value.length || 0} dongle(s) assigned to trunk-recorder.`,
    color: 'success',
  })
}

// Plan one trunk-recorder source per assigned dongle: greedily group the
// frequencies into ≤~2.16 MHz windows (what a 2.4 MHz dongle can cover), then
// centre each assigned dongle on a window. Windows containing a control channel
// are covered first so the system can always lock when dongles are scarce.
function buildTRSources(coverHz: number[], controlHz: number[], dongleDevices: string[]) {
  const sorted = [...new Set(coverHz)].sort((a, b) => a - b)
  if (!sorted.length) return { sources: [] as object[], uncovered: 0 }

  const windows: { min: number; max: number }[] = []
  let min = sorted[0]!, max = sorted[0]!
  for (const f of sorted) {
    if (f - min <= SDR_SAMPLE_RATE * 0.9) max = f
    else { windows.push({ min, max }); min = f; max = f }
  }
  windows.push({ min, max })

  const hasCtrl = (w: { min: number; max: number }) => controlHz.some(h => h >= w.min && h <= w.max)
  windows.sort((a, b) => (hasCtrl(a) ? 0 : 1) - (hasCtrl(b) ? 0 : 1))

  const devices = dongleDevices.length ? dongleDevices : ['rtl=0']
  const nCover = Math.min(windows.length, devices.length)
  const sources = windows.slice(0, nCover).map((w, i) => ({
    driver: 'osmosdr',
    device: devices[i],
    center: Math.round((w.min + w.max) / 2),
    rate: SDR_SAMPLE_RATE,
    gain: 49,
    digitalRecorders: 4,
  }))
  return { sources, uncovered: windows.length - nCover }
}

async function generateTRConfig() {
  if (!trGenSystemRef.value) {
    toast.add({ title: 'Select a system first', color: 'warning' })
    return
  }
  const controlChannels = parseFreqField(trGenControlChannels.value)
  if (!controlChannels.length) {
    toast.add({ title: 'Enter at least one control channel frequency', color: 'warning' })
    return
  }

  // Refuse to generate without an explicitly-assigned trunk-recorder dongle: the only
  // fallback would be rtl=0, which can collide with whichever physical dongle SDRangel
  // pinned to device-set 0 — both apps would fight over it.
  if (!trDongleDevices.value.length) {
    toast.add({ title: 'No trunk-recorder dongle assigned', description: 'Assign at least one dongle to trunk-recorder on the SDR Devices tab first — generating without one defaults to rtl=0 and may collide with an SDRangel dongle.', color: 'error' })
    return
  }

  // Cover the full imported frequency span if available, else just the control
  // channels. Spread the coverage across the dongles assigned to trunk-recorder.
  const coverHz = trFrequencies.value.length ? trFrequencies.value : controlChannels
  const { sources, uncovered } = buildTRSources(coverHz, controlChannels, trDongleDevices.value)

  // Dongles pinned by USB index (numeric/empty serial — see trDeviceString) don't
  // survive re-plugging; warn so the operator regenerates after any USB change.
  const indexPinned = sources.filter(s => /^rtl=\d+$/.test((s as { device: string }).device)).length
  if (indexPinned) {
    toast.add({ title: `${indexPinned} source(s) pinned by USB index`, description: 'These dongles have a numeric/empty serial, so they\'re pinned by current USB index, not serial. Re-generate this config if you replug or reorder dongles. Set unique non-numeric serials with rtl_eeprom for stable pinning.', color: 'warning' })
  }

  trGenerating.value = true
  trGenMessage.value = ''
  const result = await admin.generateTrunkRecorderConfig({
    systemRef: trGenSystemRef.value,
    controlChannels,
    systemType: trGenSystemType.value,
    sources,
  })
  trGenerating.value = false

  if (result) {
    let msg = result.saveMessage ?? 'Config generated and saved.'
    msg += ` ${sources.length} SDR source(s) from ${trDongleDevices.value.length || 1} dongle(s).`
    if (uncovered > 0) msg += ` ⚠ ${uncovered} more frequency window(s) need ${uncovered} more dongle(s) assigned to trunk-recorder.`
    trGenMessage.value = msg
    // Flag (amber + persistent) when the server dropped/confined channels or warned —
    // e.g. control channels confined to one SDR band — so it isn't a cheery green.
    const flagged = uncovered > 0 || /confined|dropped|warning|could not|failed/i.test(result.saveMessage ?? '')
    toast.add({
      title: flagged ? 'Config generated — review the note below' : 'Config generated',
      description: msg,
      color: flagged ? 'warning' : 'success',
      duration: flagged ? 0 : undefined,
    })
  }
}

// ── SDR Devices ───────────────────────────────────────────────────────────────
async function detectDongles() {
  detectingDongles.value = true
  dongles.value = await admin.getDongles()
  detectingDongles.value = false
  if (!dongles.value.length) {
    toast.add({ title: 'No RTL-SDR dongles detected', description: 'Make sure rtl-sdr tools are installed (rtl_test)', color: 'warning' })
  }
}

function dongleAssignment(index: number) {
  return bridge.value.sdrDeviceAssignments.find(a => a.index === index)?.assignTo ?? ''
}

function setDongleAssignment(dongle: RTLDongle, assignTo: '' | 'sdrangel' | 'trunk-recorder') {
  const assignments = [...bridge.value.sdrDeviceAssignments]
  const idx = assignments.findIndex(a => a.index === dongle.index)
  if (idx >= 0) {
    // Clear scanning when the dongle leaves SDRangel — it only applies there.
    const scanEnabled = assignTo === 'sdrangel' ? assignments[idx].scanEnabled : false
    assignments[idx] = { ...assignments[idx], assignTo, scanEnabled }
  } else {
    assignments.push({ index: dongle.index, serialNumber: dongle.serialNumber, assignTo })
  }
  bridge.value = { ...bridge.value, sdrDeviceAssignments: assignments }
}

function dongleScanEnabled(index: number) {
  return bridge.value.sdrDeviceAssignments.find(a => a.index === index)?.scanEnabled ?? false
}

// Toggle the per-dongle "drive with a Frequency Scanner" flag. Only meaningful for
// SDRangel-assigned dongles; provisioning ignores it otherwise.
function setDongleScan(dongle: RTLDongle, scanEnabled: boolean) {
  const assignments = [...bridge.value.sdrDeviceAssignments]
  const idx = assignments.findIndex(a => a.index === dongle.index)
  if (idx >= 0) {
    assignments[idx] = { ...assignments[idx], scanEnabled }
  } else {
    assignments.push({ index: dongle.index, serialNumber: dongle.serialNumber, assignTo: '', scanEnabled })
  }
  bridge.value = { ...bridge.value, sdrDeviceAssignments: assignments }
}

// When SDRangel dongles are unassigned the device-set count shrinks, leaving channels
// pinned to a slot that no longer exists — which would silently mis-pin a different
// dongle on the next provision. Park any such channel (Dev Set −1) so the stale index
// can't survive. Watching the count means this only fires on an actual change, and it
// edits only channels (not assignments), so it can't loop.
watch(() => sdrangelAssignments.value.length, (n) => {
  if (!bridge.value.channels.some(c => c.deviceSetIndex >= n)) return
  bridge.value = {
    ...bridge.value,
    channels: bridge.value.channels.map(c => (c.deviceSetIndex >= n ? { ...c, deviceSetIndex: -1 } : c)),
  }
})

// ── Bridge channels ───────────────────────────────────────────────────────────
const systemOptions = computed(() =>
  (props.systems ?? []).map(s => ({ label: s.label, value: s.systemRef })),
)

// Precompute talkgroup options per system once, as stable array references, so
// the per-row `:items="talkgroupOptions(ch.systemRef)"` binding doesn't rebuild
// a fresh array on every render. This alone was NOT enough: the picker is a
// plain USelect that renders every option into the DOM when opened, so a system
// with a few hundred+ talkgroups froze the tab for seconds on open ("Bridge tab
// breaks the browser"). The real fix is the USelectMenu below — searchable +
// `:virtualize`, so it only mounts the handful of rows actually on screen.
const talkgroupOptionsBySystem = computed(() => {
  const map = new Map<number, { label: string; value: number }[]>()
  for (const s of (props.systems ?? [])) {
    map.set(s.systemRef, (s.talkgroups ?? []).map(tg => ({
      label: `${tg.talkgroupRef} – ${tg.name || tg.label}`,
      value: tg.talkgroupRef,
    })))
  }
  return map
})

function talkgroupOptions(systemRef: number) {
  return talkgroupOptionsBySystem.value.get(systemRef) ?? []
}

const protocolOptions = [
  { label: 'NFM (narrowband FM)', value: 'nfm' },
  { label: 'AM', value: 'am' },
  { label: 'USB', value: 'usb' },
  { label: 'LSB', value: 'lsb' },
]

function addChannel() {
  const maxPort = bridge.value.channels.reduce((m, c) => Math.max(m, c.udpPort), 50000)
  bridge.value = {
    ...bridge.value,
    channels: [...bridge.value.channels, {
      channelIndex: 0,
      deviceSetIndex: 0,
      frequencyHz: 0,
      label: '',
      protocol: 'nfm',
      squelchDb: -45,
      sampleRate: 8000,
      systemRef: 0,
      talkgroupRef: 0,
      udpPort: maxPort + 1,
      scan: false,
    }],
  }
}

// ── Quick-add an existing system ──────────────────────────────────────────────
// Bridging a conventional system one row at a time (re-selecting the same system
// + each talkgroup) is tedious. quickAddSystemRef + addSystemChannels() appends
// one bridge channel per talkgroup of the chosen system, prefilling label,
// frequency, system, and talkgroup, and skipping talkgroups already bridged.
const quickAddSystemRef = ref<number>(0)

const quickAddOptions = computed(() => [
  { label: '— add a system —', value: 0 },
  ...systemOptions.value,
])

// Lowest free UDP port in the bridge pool [50000, 65000]; tracks `used` so a
// bulk add hands out distinct ports. 0 if exhausted — the server's
// normalizeBridgePorts() reassigns any 0/out-of-range/duplicate port on save.
function nextFreePort(used: Set<number>): number {
  for (let p = 50000; p <= 65000; p++) {
    if (!used.has(p)) { used.add(p); return p }
  }
  return 0
}

function addSystemChannels() {
  const sys = (props.systems ?? []).find(s => s.systemRef === quickAddSystemRef.value)
  if (!sys) {
    toast.add({ title: 'Select a system to add', color: 'warning' })
    return
  }
  const talkgroups = sys.talkgroups ?? []
  if (!talkgroups.length) {
    toast.add({ title: `${sys.label} has no talkgroups`, color: 'warning' })
    return
  }

  // Don't re-add a talkgroup that already has a bridge channel.
  const existing = new Set(bridge.value.channels.map(c => `${c.systemRef}:${c.talkgroupRef}`))
  const used = new Set(
    bridge.value.channels.map(c => c.udpPort).filter(p => p >= 50000 && p <= 65000),
  )

  const added: BridgeChannel[] = []
  let skipped = 0
  let noFreq = 0
  for (const tg of talkgroups) {
    const key = `${sys.systemRef}:${tg.talkgroupRef}`
    if (existing.has(key)) { skipped++; continue }
    existing.add(key)
    const freq = tg.frequency ?? 0
    if (!freq) noFreq++
    added.push({
      channelIndex: 0,
      deviceSetIndex: 0,
      frequencyHz: freq,
      label: tg.name || tg.label || `TG ${tg.talkgroupRef}`,
      protocol: 'nfm',
      squelchDb: -45,
      sampleRate: 8000,
      systemRef: sys.systemRef,
      talkgroupRef: tg.talkgroupRef,
      udpPort: nextFreePort(used),
      scan: false,
    })
  }

  if (!added.length) {
    toast.add({ title: `All ${talkgroups.length} talkgroup(s) from ${sys.label} are already bridged`, color: 'info' })
    return
  }
  bridge.value = { ...bridge.value, channels: [...bridge.value.channels, ...added] }

  const title = skipped
    ? `Added ${added.length} channel(s) from ${sys.label} · ${skipped} already present`
    : `Added ${added.length} channel(s) from ${sys.label}`
  toast.add({
    title,
    description: noFreq
      ? `${noFreq} talkgroup(s) had no frequency — fill in Freq (Hz) before provisioning. Then Auto-assign SDRs, Save, Provision.`
      : 'Review, Auto-assign SDRs, Save, then Provision.',
    color: noFreq ? 'warning' : 'success',
  })
}

function removeChannel(i: number) {
  const channels = [...bridge.value.channels]
  channels.splice(i, 1)
  bridge.value = { ...bridge.value, channels }
}

// Wipe the whole channel list in one click. Confirmed because it can drop a
// large config, but it's only the edit buffer — Save persists, reload undoes.
function removeAllChannels() {
  const n = bridge.value.channels.length
  if (!n) return
  if (!window.confirm(`Remove all ${n} bridge channel${n > 1 ? 's' : ''}? Save to persist this, or reload the page to undo.`)) return
  bridge.value = { ...bridge.value, channels: [] }
}

// Flip the Scan flag on every channel at once (the "enable/disable all" controls).
function setAllScan(scan: boolean) {
  if (!bridge.value.channels.length) return
  bridge.value = { ...bridge.value, channels: bridge.value.channels.map(c => ({ ...c, scan })) }
}

const scanChannelCount = computed(() => bridge.value.channels.filter(c => c.scan).length)

function scanStateLabel(s: number): string {
  switch (s) {
    case 2: return 'scanning'
    case 3: return 'receiving'
    default: return 'idle'
  }
}

// Usable RF span one RTL-SDR dongle can cover at a 2.4 MHz sample rate, leaving
// margin for filter rolloff and the centre DC spike.
const SDR_USABLE_HZ = 2_000_000

// clusterToSets greedily packs channels into ≤SDR_USABLE_HZ frequency windows (one
// dongle covers ~2 MHz) and maps window k to positions[k]. Channels whose window
// has no available device set are parked (-1). Returns the per-channel mapping plus
// how many were parked.
function clusterToSets(chans: BridgeChannel[], positions: number[]) {
  const map = new Map<BridgeChannel, number>()
  if (!chans.length) return { map, parked: 0 }
  const sorted = [...chans].sort((a, b) => a.frequencyHz - b.frequencyHz)
  const starts: number[] = [sorted[0]!.frequencyHz]
  for (const ch of sorted) {
    if (ch.frequencyHz - starts[starts.length - 1]! > SDR_USABLE_HZ) starts.push(ch.frequencyHz)
  }
  const windowFor = (hz: number) => {
    for (let i = starts.length - 1; i >= 0; i--) if (hz >= starts[i]!) return i
    return 0
  }
  let parked = 0
  for (const ch of chans) {
    const w = windowFor(ch.frequencyHz)
    const pos = w < positions.length ? positions[w]! : -1
    if (pos < 0) parked++
    map.set(ch, pos)
  }
  return { map, parked }
}

// autoAssignDevices fills the Dev Set column scan-aware: Scan channels are clustered
// onto the dongles marked Scanning (a scan channel may run ONLY on a scanner dongle),
// and fixed channels onto the remaining SDRangel dongles (falling back to scanner
// dongles, which can also host fixed UDPSinks). Channels with no matching dongle are
// parked (Dev Set −1). The user then Saves and Provisions.
function autoAssignDevices() {
  const withFreq = bridge.value.channels.filter(c => c.frequencyHz > 0)
  if (!withFreq.length) {
    toast.add({ title: 'Add channels with frequencies first', color: 'warning' })
    return
  }

  const scannerSets = scannerSetIndices.value
  let plainSets = sdrangelAssignments.value.map((a, i) => ({ a, i })).filter(x => !x.a.scanEnabled).map(x => x.i)
  // No assignment made yet: treat detected dongles as plain sets so fixed channels
  // still auto-assign. Scanning genuinely needs a dongle explicitly marked Scanning.
  if (!sdrangelAssignments.value.length) {
    plainSets = Array.from({ length: dongles.value.length || 4 }, (_, i) => i)
  }

  const scanChans = withFreq.filter(c => c.scan)
  const fixedChans = withFreq.filter(c => !c.scan)
  const scanRes = clusterToSets(scanChans, scannerSets)
  // Fixed channels prefer plain dongles; with none, they may ride a scanner dongle.
  const fixedRes = clusterToSets(fixedChans, plainSets.length ? plainSets : scannerSets)

  bridge.value = {
    ...bridge.value,
    channels: bridge.value.channels.map(c => {
      if (c.frequencyHz <= 0) return c
      const idx = c.scan ? (scanRes.map.get(c) ?? -1) : (fixedRes.map.get(c) ?? -1)
      return { ...c, deviceSetIndex: idx }
    }),
  }

  const placed = withFreq.length - scanRes.parked - fixedRes.parked
  if (scanChans.length && !scannerSets.length) {
    toast.add({
      title: `${scanChans.length} scan channel(s) need a scanner dongle`,
      description: 'No dongle has Scanning enabled. Mark one on the SDR Devices tab, then Auto-assign again. Other channels were assigned.',
      color: 'warning',
    })
  } else if (scanRes.parked || fixedRes.parked) {
    toast.add({
      title: `Assigned ${placed}, parked ${scanRes.parked + fixedRes.parked}`,
      description: `Parked channels (Dev Set −1) need more SDRangel dongles: ${scanRes.parked} scan / ${fixedRes.parked} fixed. Each dongle covers ~${(SDR_USABLE_HZ / 1e6).toFixed(1)} MHz.`,
      color: 'warning',
    })
  } else {
    toast.add({
      title: `Assigned ${placed} channel(s)`,
      description: 'Scan channels on scanner dongle(s), fixed channels on the rest. Review the Dev Set column, Save, then Provision.',
      color: 'success',
    })
  }
}

// ── Helpers ───────────────────────────────────────────────────────────────────
function formatUptime(seconds?: number) {
  if (!seconds) return ''
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  if (h) return `${h}h ${m}m`
  if (m) return `${m}m ${s}s`
  return `${s}s`
}

const trSystemOptions = computed(() => [
  { label: '— select system —', value: 0 },
  ...(props.systems ?? []).map(s => ({ label: s.label, value: s.systemRef })),
])
</script>

<template>
  <div class="space-y-4">
    <!-- Sub-tab navigation -->
    <div class="flex flex-wrap gap-1 border-b border-neutral-800 pb-2">
      <button
        v-for="tab in [
          { key: 'sdrangel', label: 'SDRangel' },
          { key: 'trunk-recorder', label: 'Trunk-Recorder' },
          { key: 'devices', label: 'SDR Devices' },
          { key: 'channels', label: 'Bridge Channels' },
        ]"
        :key="tab.key"
        class="px-3 py-1.5 rounded-md text-sm transition-colors"
        :class="subTab === tab.key
          ? 'bg-neutral-700 text-white'
          : 'text-neutral-400 hover:text-white hover:bg-neutral-800'"
        @click="subTab = tab.key as typeof subTab"
      >
        {{ tab.label }}
        <span
          v-if="tab.key === 'sdrangel' && sdrangelSvc.running"
          class="ml-1.5 inline-block w-1.5 h-1.5 rounded-full bg-green-500 align-middle"
        />
        <span
          v-if="tab.key === 'trunk-recorder' && trSvc.running"
          class="ml-1.5 inline-block w-1.5 h-1.5 rounded-full bg-green-500 align-middle"
        />
        <span
          v-if="tab.key === 'channels' && bridgeSvc.running"
          class="ml-1.5 inline-block w-1.5 h-1.5 rounded-full bg-blue-500 align-middle"
        />
      </button>
    </div>

    <!-- ── SDRangel tab ─────────────────────────────────────────────────────── -->
    <div v-if="subTab === 'sdrangel'" class="space-y-5">
      <!-- Service status card -->
      <div class="rounded-lg border border-neutral-800 p-4">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <span
              class="inline-block w-2.5 h-2.5 rounded-full"
              :class="sdrangelSvc.running ? 'bg-green-500' : 'bg-neutral-600'"
            />
            <span class="font-semibold text-sm">
              sdrangelsrv — {{ sdrangelSvc.running ? 'Running' : 'Stopped' }}
            </span>
            <span v-if="sdrangelSvc.mode" class="text-xs text-neutral-500 font-mono">
              [{{ sdrangelSvc.mode }}]
            </span>
            <span v-if="sdrangelSvc.pid" class="text-xs text-neutral-500">
              PID {{ sdrangelSvc.pid }}
            </span>
            <span v-if="sdrangelSvc.uptimeSeconds" class="text-xs text-neutral-500">
              · up {{ formatUptime(sdrangelSvc.uptimeSeconds) }}
            </span>
          </div>
          <div class="flex gap-2">
            <UButton size="xs" :loading="sdrangelActioning" variant="soft" color="success"
              :disabled="sdrangelSvc.running" @click="sdrangelAction('start')">Start</UButton>
            <UButton size="xs" :loading="sdrangelActioning" variant="soft" color="error"
              :disabled="!sdrangelSvc.running" @click="sdrangelAction('stop')">Stop</UButton>
            <UButton size="xs" :loading="sdrangelActioning" variant="soft"
              @click="sdrangelAction('restart')">Restart</UButton>
          </div>
        </div>
        <p v-if="sdrangelSvc.message" class="text-xs text-neutral-500">{{ sdrangelSvc.message }}</p>
      </div>

      <!-- Provisioned state: what SDRangel actually has configured right now -->
      <div class="rounded-lg border border-neutral-800 p-4">
        <div class="flex items-center justify-between mb-2">
          <p class="text-xs font-semibold text-neutral-400 uppercase tracking-wide">Provisioned on SDRangel</p>
          <span
            class="text-xs font-mono"
            :class="provisioning ? 'text-amber-400' : (sdrangelStatus.connected ? 'text-green-500' : 'text-neutral-500')"
          >
            {{ provisioning ? 'provisioning…' : (sdrangelStatus.connected ? 'live' : 'unreachable') }}
          </span>
        </div>

        <p v-if="!sdrangelStatus.connected" class="text-xs text-neutral-500">
          Can't read SDRangel — start sdrangelsrv to see what's provisioned.
        </p>
        <p v-else-if="!provisionedChannelCount" class="text-xs text-neutral-500">
          Nothing provisioned yet — set up channels below, then click Provision SDRangel.
        </p>
        <div v-else class="space-y-2">
          <p class="text-xs text-neutral-400">
            {{ (sdrangelStatus.deviceSets ?? []).length }} device set(s) · {{ provisionedChannelCount }} channel(s)
          </p>
          <div v-for="ds in provisionedDeviceSets" :key="ds.index" class="text-xs">
            <span class="font-mono text-neutral-300">Device set {{ ds.index }}</span>
            <span class="text-neutral-500"> · {{ ds.hwType || 'unassigned' }} · {{ ds.channels.length }} ch</span>
            <div v-if="ds.channels.length" class="mt-1 pl-3 flex flex-wrap gap-1">
              <span
                v-for="ch in ds.channels"
                :key="ch.index"
                class="rounded bg-neutral-800 px-1.5 py-0.5 text-neutral-300 font-mono"
              >{{ ch.label }}<span
                v-if="ch.freqHz || ch.udpPort"
                class="text-neutral-500"
              >{{ ch.freqHz ? ' · ' + formatMHz(ch.freqHz) : '' }}{{ ch.udpPort ? ' · :' + ch.udpPort : '' }}</span></span>
            </div>
          </div>

          <!-- Live Frequency Scanner state -->
          <div v-if="(sdrangelStatus.scanners ?? []).length" class="space-y-1.5 pt-2 border-t border-neutral-800/60">
            <p class="text-[11px] font-semibold text-neutral-500 uppercase tracking-wide">Frequency Scanners</p>
            <div v-for="sc in sdrangelStatus.scanners" :key="`${sc.deviceSetIndex}-${sc.channelIndex}`" class="text-xs">
              <span class="font-mono text-neutral-300">Scanner ds{{ sc.deviceSetIndex }}</span>
              <span class="text-neutral-500"> · {{ scanStateLabel(sc.scanState) }}</span>
              <span v-if="sc.activeFreqHz" class="text-green-400"> · ▶ {{ formatMHz(sc.activeFreqHz) }}</span>
              <div v-if="(sc.frequencies ?? []).length" class="mt-1 pl-3 flex flex-wrap gap-1">
                <span
                  v-for="(f, idx) in sc.frequencies"
                  :key="`${sc.deviceSetIndex}-${f.frequencyHz}-${idx}`"
                  class="rounded px-1.5 py-0.5 font-mono"
                  :class="sc.activeFreqHz === f.frequencyHz ? 'bg-green-900 text-green-300' : 'bg-neutral-800 text-neutral-400'"
                >{{ f.label || formatMHz(f.frequencyHz) }}<span class="text-neutral-600"> {{ Math.round(f.powerDb) }}dB</span></span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Connection settings -->
      <div>
        <p class="text-xs font-semibold text-neutral-400 uppercase tracking-wide mb-2">Connection</p>
        <div class="grid grid-cols-3 gap-3">
          <UFormField label="Host">
            <UInput v-model="bridge.host" placeholder="127.0.0.1" />
          </UFormField>
          <UFormField label="Port">
            <UInput v-model.number="bridge.port" type="number" placeholder="8091" />
          </UFormField>
          <UFormField label="">
            <div class="flex items-center gap-3 mt-6">
              <UCheckbox v-model="bridge.enabled" label="Bridge enabled" />
            </div>
          </UFormField>
        </div>
      </div>

      <!-- Native binary / Docker container -->
      <div>
        <p class="text-xs font-semibold text-neutral-400 uppercase tracking-wide mb-2">Process</p>
        <div class="grid grid-cols-2 gap-3">
          <UFormField label="Binary Path" description="Path to sdrangelsrv (native, recommended for Pi)">
            <UInput v-model="bridge.sdrangelBinaryPath" placeholder="/usr/bin/sdrangelsrv" />
          </UFormField>
          <UFormField label="Container Name" description="Docker container name (if using Docker)">
            <UInput v-model="bridge.sdrangelContainerName" placeholder="sdrangelsrv" />
          </UFormField>
        </div>
      </div>

      <!-- Provision button -->
      <div class="flex items-center gap-3">
        <UButton
          icon="i-heroicons-bolt"
          variant="soft"
          :loading="provisioning"
          :disabled="!bridge.enabled || !bridge.channels.length"
          @click="provision"
        >
          Provision SDRangel
        </UButton>
        <!-- Cancel an in-flight run so a mistake can be undone and restarted. -->
        <UButton
          v-if="provisioning"
          icon="i-heroicons-x-mark"
          color="error"
          variant="soft"
          :loading="cancelling"
          @click="cancelProvision"
        >
          Cancel
        </UButton>
        <!-- Clear a finished run's output (also wipes the server-side status). -->
        <UButton
          v-else-if="provisionMessages.length"
          icon="i-heroicons-trash"
          color="neutral"
          variant="ghost"
          :loading="cancelling"
          @click="cancelProvision"
        >
          Clear
        </UButton>
        <span class="text-xs text-neutral-500">
          Pushes channel config to the running SDRangel instance
        </span>
      </div>

      <!-- Provision output -->
      <div v-if="provisionMessages.length">
        <p class="text-xs font-semibold text-neutral-400 uppercase tracking-wide mb-1.5">Last Provision</p>
        <div class="bg-neutral-950 rounded border border-neutral-800 font-mono text-xs p-3 max-h-48 overflow-y-auto space-y-0.5">
          <div
            v-for="(line, i) in provisionMessages"
            :key="i"
            class="leading-relaxed"
            :class="/failed|warning|cannot/i.test(line) ? 'text-amber-400' : 'text-neutral-300'"
          >{{ line }}</div>
        </div>
      </div>

      <!-- Live logs -->
      <div>
        <div class="flex items-center justify-between mb-1.5">
          <p class="text-xs font-semibold text-neutral-400 uppercase tracking-wide">Live Logs</p>
          <UButton size="xs" variant="ghost" icon="i-heroicons-arrow-path" @click="refreshSDRangelLogs">
            Refresh
          </UButton>
        </div>
        <div
          ref="sdrangelLogsEl"
          class="bg-neutral-950 rounded border border-neutral-800 font-mono text-xs p-3 h-48 overflow-y-auto space-y-0.5"
        >
          <div v-for="(line, i) in sdrangelLogs" :key="i" class="text-neutral-300 leading-relaxed">{{ line }}</div>
          <div v-if="!sdrangelLogs.length" class="text-neutral-600">No log output captured. (Only an sdrangelsrv that rdio-scanner started itself is captured here — an externally-launched instance shows nothing.)</div>
        </div>
      </div>
    </div>

    <!-- ── Trunk-Recorder tab ──────────────────────────────────────────────── -->
    <div v-else-if="subTab === 'trunk-recorder'" class="space-y-5">
      <!-- Service status card -->
      <div class="rounded-lg border border-neutral-800 p-4">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <span
              class="inline-block w-2.5 h-2.5 rounded-full"
              :class="trSvc.running ? 'bg-green-500' : 'bg-neutral-600'"
            />
            <span class="font-semibold text-sm">
              trunk-recorder — {{ trSvc.running ? 'Running' : 'Stopped' }}
            </span>
            <span v-if="trSvc.mode" class="text-xs text-neutral-500 font-mono">
              [{{ trSvc.mode }}]
            </span>
            <span v-if="trSvc.pid" class="text-xs text-neutral-500">
              PID {{ trSvc.pid }}
            </span>
          </div>
          <div class="flex gap-2">
            <UButton size="xs" :loading="trActioning" variant="soft" color="success"
              :disabled="trSvc.running" @click="trAction('start')">Start</UButton>
            <UButton size="xs" :loading="trActioning" variant="soft" color="error"
              :disabled="!trSvc.running" @click="trAction('stop')">Stop</UButton>
            <UButton size="xs" :loading="trActioning" variant="soft"
              @click="trAction('restart')">Restart</UButton>
          </div>
        </div>
        <p v-if="trSvc.message" class="text-xs text-neutral-500">{{ trSvc.message }}</p>
      </div>

      <!-- Paths are managed automatically (binary at the install location, config
           in the server's data dir) so there's nothing to configure or get wrong. -->
      <p class="text-xs text-neutral-500">
        trunk-recorder runs from its default install location and its config is
        generated and stored automatically — no paths to configure.
      </p>

      <!-- Config generator -->
      <div class="rounded-lg border border-neutral-800 p-4 space-y-3">
        <p class="text-sm font-semibold text-neutral-300">Generate Config</p>
        <div class="grid grid-cols-2 gap-3">
          <UFormField label="System">
            <USelect v-model.number="trGenSystemRef" :items="trSystemOptions" />
          </UFormField>
          <UFormField label="System Type">
            <USelect v-model="trGenSystemType" :items="[
              { label: 'P25 Phase 1', value: 'P25' },
              { label: 'P25 Phase 2', value: 'P25p2' },
              { label: 'DMR', value: 'dmr' },
              { label: 'EDACS', value: 'edacs' },
            ]" />
          </UFormField>
        </div>
        <UFormField label="Import RadioReference Sites CSV (optional)" description="Auto-fills control channels and plans multi-dongle SDR coverage from a trs_sites_*.csv export">
          <input type="file" accept=".csv,.txt" class="text-xs text-neutral-400" @change="importSitesCSV" >
        </UFormField>
        <p v-if="trSitesFileName" class="text-xs text-neutral-500 -mt-1">
          Loaded <span class="font-mono">{{ trSitesFileName }}</span> — {{ trFrequencies.length }} frequencies.
          {{ trDongleDevices.length }} dongle(s) assigned to trunk-recorder
          <span v-if="!trDongleDevices.length" class="text-yellow-500">(assign dongles on the SDR Devices tab for multi-SDR coverage)</span>.
        </p>

        <UFormField label="Control Channels (MHz or Hz, comma-separated)" description="e.g. 851.0125, 851.5125 or 851012500, 851512500">
          <UInput v-model="trGenControlChannels" placeholder="851.0125, 851.5125" />
        </UFormField>
        <div class="flex items-center gap-3">
          <UButton
            icon="i-heroicons-cog-6-tooth"
            variant="soft"
            :loading="trGenerating"
            :disabled="!trGenSystemRef"
            @click="generateTRConfig"
          >
            Generate & Save Config
          </UButton>
          <span class="text-xs text-neutral-500">
            Saves to Config File Path above (if set)
          </span>
        </div>
        <p v-if="trGenMessage" class="text-xs text-green-400">{{ trGenMessage }}</p>
      </div>

      <!-- Live logs -->
      <div>
        <div class="flex items-center justify-between mb-1.5">
          <p class="text-xs font-semibold text-neutral-400 uppercase tracking-wide">Live Logs</p>
          <UButton size="xs" variant="ghost" icon="i-heroicons-arrow-path" @click="refreshTRLogs">
            Refresh
          </UButton>
        </div>
        <div
          ref="trLogsEl"
          class="bg-neutral-950 rounded border border-neutral-800 font-mono text-xs p-3 h-48 overflow-y-auto space-y-0.5"
        >
          <div v-for="(line, i) in trLogs" :key="i" class="text-neutral-300 leading-relaxed">{{ line }}</div>
          <div v-if="!trLogs.length" class="text-neutral-600">No log output captured.</div>
        </div>
      </div>
    </div>

    <!-- ── SDR Devices tab ─────────────────────────────────────────────────── -->
    <div v-else-if="subTab === 'devices'" class="space-y-4">
      <div class="flex items-center justify-between">
        <div>
          <p class="text-sm font-semibold text-neutral-300">Detected RTL-SDR Dongles</p>
          <p class="text-xs text-neutral-500 mt-0.5">Assign each dongle to SDRangel (analog/digital) or trunk-recorder (P25 trunked). Requires <span class="font-mono">rtl-sdr</span> tools installed.</p>
        </div>
        <UButton size="sm" variant="soft" icon="i-heroicons-magnifying-glass" :loading="detectingDongles" @click="detectDongles">
          Detect
        </UButton>
      </div>

      <!-- Dongle-identity warnings: missing/duplicate serials break reliable pinning. -->
      <div v-if="sdrAssignmentWarnings.length" class="rounded border border-amber-700/50 bg-amber-950/30 px-3 py-2 space-y-1">
        <p v-for="(w, i) in sdrAssignmentWarnings" :key="i" class="text-xs text-amber-400 flex items-start gap-1.5">
          <UIcon name="i-heroicons-exclamation-triangle" class="shrink-0 mt-0.5" />
          <span>{{ w }}</span>
        </p>
      </div>

      <div v-if="dongles.length" class="rounded border border-neutral-800 overflow-x-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="bg-neutral-900 text-neutral-500">
              <th class="px-3 py-2 text-left">#</th>
              <th class="px-3 py-2 text-left">Manufacturer</th>
              <th class="px-3 py-2 text-left">Product</th>
              <th class="px-3 py-2 text-left">Serial</th>
              <th class="px-3 py-2 text-left">Assign To</th>
              <th class="px-3 py-2 text-left">Scanning</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="dongle in dongles" :key="dongle.index" class="border-t border-neutral-800">
              <td class="px-3 py-2 font-mono text-neutral-400">{{ dongle.index }}</td>
              <td class="px-3 py-2">{{ dongle.manufacturer }}</td>
              <td class="px-3 py-2">{{ dongle.product }}</td>
              <td class="px-3 py-2 font-mono text-neutral-400">{{ dongle.serialNumber }}</td>
              <td class="px-3 py-2">
                <USelect
                  :model-value="dongleAssignment(dongle.index) || 'unassigned'"
                  size="xs"
                  :items="[
                    { label: 'Unassigned', value: 'unassigned' },
                    { label: 'SDRangel', value: 'sdrangel' },
                    { label: 'Trunk-Recorder', value: 'trunk-recorder' },
                  ]"
                  @update:model-value="v => setDongleAssignment(dongle, (v === 'unassigned' ? '' : v) as '' | 'sdrangel' | 'trunk-recorder')"
                />
              </td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1.5">
                  <UCheckbox
                    :model-value="dongleScanEnabled(dongle.index)"
                    :disabled="dongleAssignment(dongle.index) !== 'sdrangel'"
                    @update:model-value="v => setDongleScan(dongle, v === true)"
                  />
                  <span v-if="dongleAssignment(dongle.index) !== 'sdrangel'" class="text-[10px] text-neutral-600">SDRangel only</span>
                  <span v-else class="text-[10px] text-neutral-500">Frequency Scanner</span>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="dongles.length" class="text-xs text-neutral-500">
        Enable <span class="text-neutral-400">Scanning</span> on an SDRangel dongle to drive it with a single Frequency
        Scanner that hops the channels you mark <span class="text-neutral-400">Scan</span> (Bridge Channels tab) instead of
        running one demodulator per channel — fits more frequencies on one dongle, but receives only one at a time.
      </p>

      <div v-else class="text-center py-8 text-neutral-600 text-sm">
        No dongles detected yet. Click Detect to scan for connected RTL-SDR devices.
      </div>
    </div>

    <!-- ── Bridge Channels tab ─────────────────────────────────────────────── -->
    <div v-else-if="subTab === 'channels'" class="space-y-4">
      <!-- Bridge relay status -->
      <div class="flex items-center gap-3 rounded-lg border border-neutral-800 p-3">
        <span
          class="inline-block w-2.5 h-2.5 rounded-full flex-shrink-0"
          :class="bridgeSvc.running ? 'bg-blue-500' : 'bg-neutral-600'"
        />
        <div class="text-sm">
          <span class="font-semibold">SDRangel Bridge</span>
          <span class="text-neutral-400 ml-2">
            {{ bridgeSvc.running ? `Active — ${bridgeSvc.channelCount} channel(s) monitored` : 'Inactive' }}
          </span>
          <span v-if="!bridge.enabled" class="ml-2 text-yellow-500 text-xs">(bridge disabled)</span>
        </div>
      </div>

      <div class="flex items-center justify-between">
        <span class="text-sm font-semibold text-neutral-300">Channels ({{ bridge.channels.length }}<span v-if="scanChannelCount" class="font-normal text-neutral-500"> · {{ scanChannelCount }} scan</span>)</span>
        <div class="flex flex-wrap items-center gap-2">
          <div class="flex items-center gap-1">
            <USelect
              v-model.number="quickAddSystemRef"
              :items="quickAddOptions"
              size="xs"
              class="w-44"
            />
            <UButton
              icon="i-heroicons-rectangle-stack"
              size="xs"
              variant="ghost"
              :disabled="!quickAddSystemRef"
              @click="addSystemChannels"
            >
              Add System
            </UButton>
          </div>
          <span class="text-neutral-700">|</span>
          <UButton icon="i-heroicons-plus" size="xs" variant="ghost" @click="addChannel">
            Add Channel
          </UButton>
          <UButton
            icon="i-heroicons-trash"
            size="xs"
            variant="ghost"
            color="error"
            :disabled="!bridge.channels.length"
            @click="removeAllChannels"
          >
            Remove All
          </UButton>
          <UButton
            icon="i-heroicons-cpu-chip"
            size="xs"
            variant="ghost"
            :disabled="!bridge.channels.length"
            @click="autoAssignDevices"
          >
            Auto-assign SDRs
          </UButton>
          <span class="text-neutral-700">|</span>
          <UButton
            icon="i-heroicons-check-circle"
            size="xs"
            variant="ghost"
            :disabled="!bridge.channels.length"
            @click="setAllScan(true)"
          >
            Scan all
          </UButton>
          <UButton
            icon="i-heroicons-no-symbol"
            size="xs"
            variant="ghost"
            :disabled="!bridge.channels.length"
            @click="setAllScan(false)"
          >
            Scan none
          </UButton>
          <UButton
            icon="i-heroicons-bolt"
            size="xs"
            variant="soft"
            :loading="provisioning"
            :disabled="!bridge.enabled || !bridge.channels.length"
            @click="provision"
          >
            Provision SDRangel
          </UButton>
          <UButton
            v-if="provisioning"
            icon="i-heroicons-x-mark"
            size="xs"
            color="error"
            variant="soft"
            :loading="cancelling"
            @click="cancelProvision"
          >
            Cancel
          </UButton>
        </div>
      </div>

      <div class="rounded border border-neutral-800 overflow-x-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="bg-neutral-900 text-neutral-500">
              <th class="px-2 py-1.5 text-center" title="Include in this device set's Frequency Scanner">Scan</th>
              <th class="px-2 py-1.5 text-left">Label</th>
              <th class="px-2 py-1.5 text-left">Freq (Hz)</th>
              <th class="px-2 py-1.5 text-left">Protocol</th>
              <th class="px-2 py-1.5 text-left">Squelch (dB)</th>
              <th class="px-2 py-1.5 text-left">System</th>
              <th class="px-2 py-1.5 text-left">Talkgroup</th>
              <th class="px-2 py-1.5 text-left">UDP Port</th>
              <th class="px-2 py-1.5 text-left">Dev Set</th>
              <th class="px-2 py-1.5" />
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="(ch, i) in bridge.channels"
              :key="i"
              class="border-t border-neutral-800"
            >
              <td class="px-2 py-1 text-center"><UCheckbox v-model="ch.scan" /></td>
              <td class="px-2 py-1"><UInput v-model="ch.label" size="xs" /></td>
              <td class="px-2 py-1"><UInput v-model.number="ch.frequencyHz" type="number" size="xs" /></td>
              <td class="px-2 py-1"><USelect v-model="ch.protocol" :items="protocolOptions" size="xs" /></td>
              <td class="px-2 py-1"><UInput v-model.number="ch.squelchDb" type="number" size="xs" /></td>
              <td class="px-2 py-1"><USelect v-model.number="ch.systemRef" :items="systemOptions" size="xs" /></td>
              <td class="px-2 py-1">
                <USelectMenu
                  v-model="ch.talkgroupRef"
                  :items="talkgroupOptions(ch.systemRef)"
                  value-key="value"
                  :virtualize="true"
                  :search-input="{ placeholder: 'Search talkgroups…' }"
                  size="xs"
                  placeholder="—"
                  class="w-44"
                />
              </td>
              <td class="px-2 py-1"><UInput v-model.number="ch.udpPort" type="number" size="xs" /></td>
              <td class="px-2 py-1">
                <USelect v-model.number="ch.deviceSetIndex" :items="deviceSetOptions" size="xs" class="w-40" />
                <span v-if="ch.deviceSetIndex < 0" class="block text-[10px] text-amber-500 mt-0.5">unassigned</span>
                <span v-else-if="ch.scan && !scannerSetIndices.includes(ch.deviceSetIndex)" class="block text-[10px] text-amber-500 mt-0.5">not a scanner — will move on provision</span>
              </td>
              <td class="px-2 py-1">
                <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="xs"
                  @click="removeChannel(i)" />
              </td>
            </tr>
            <tr v-if="!bridge.channels.length">
              <td colspan="10" class="px-3 py-6 text-center text-neutral-600">
                No channels — add one above, then click Provision SDRangel.
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <p class="text-xs text-neutral-600">
        Each channel maps one SDRangel NFM/AM/USB/LSB demodulator to a rdio-scanner talkgroup via UDP audio relay.
        Squelch (dB) is the per-channel threshold SDRangel uses to gate audio (default −45). If you get constant
        static or never-ending calls, the squelch is passing noise — raise it toward −35/−30. Lower it (e.g. −55)
        only if real transmissions don't open it. After editing, save config and click Provision SDRangel.
      </p>
    </div>
  </div>
</template>
