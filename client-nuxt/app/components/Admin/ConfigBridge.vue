<script setup lang="ts">
import type {
  BridgeConfig,
  BridgeChannel,
  AdminSystem,
  RTLDongle,
  SDRangelServiceStatus,
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
// Dongles the operator assigned to trunk-recorder on the SDR Devices tab.
const trDongleIndices = computed(() =>
  bridge.value.sdrDeviceAssignments
    .filter(a => a.assignTo === 'trunk-recorder')
    .map(a => a.index)
    .sort((a, b) => a - b))

// ── Polling ───────────────────────────────────────────────────────────────────
let pollTimer: ReturnType<typeof setInterval> | null = null
let provisionPollTimer: ReturnType<typeof setTimeout> | null = null

async function refreshAll() {
  const [svc, tr, br] = await Promise.all([
    admin.getSDRangelServiceStatus(),
    admin.getTRServiceStatus(),
    admin.getBridgeStatus(),
  ])
  sdrangelSvc.value = svc
  trSvc.value = tr
  bridgeSvc.value = br
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

async function provision() {
  provisioning.value = true

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
  // SDRangel uses only its dongles and leaves the trunk-recorder ones alone.
  const sdrangelSerials = bridge.value.sdrDeviceAssignments
    .filter(a => a.assignTo === 'sdrangel')
    .map(a => a.serialNumber)

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
    return { index, hwType: 'RTLSDR', sequence: index, serial: sdrangelSerials[index] ?? '', centerFrequencyHz: center, sampleRateHz: SDR_SAMPLE_RATE }
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

// resumeProvisionIfRunning re-attaches the live poll to a provision still running
// server-side (e.g. after a tab reload or switching back to the SDRangel sub-tab).
async function resumeProvisionIfRunning() {
  if (provisioning.value) return // already polling
  const status = await admin.getSDRangelProvisionStatus()
  if (status?.running) {
    if (status.messages.length) provisionMessages.value = status.messages
    provisioning.value = true
    if (provisionPollTimer) clearTimeout(provisionPollTimer)
    void pollProvision()
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
    description: `${ctrl.length} control channel(s); ${trDongleIndices.value.length || 0} dongle(s) assigned to trunk-recorder.`,
    color: 'success',
  })
}

// Plan one trunk-recorder source per assigned dongle: greedily group the
// frequencies into ≤~2.16 MHz windows (what a 2.4 MHz dongle can cover), then
// centre each assigned dongle on a window. Windows containing a control channel
// are covered first so the system can always lock when dongles are scarce.
function buildTRSources(coverHz: number[], controlHz: number[], dongleIndices: number[]) {
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

  const indices = dongleIndices.length ? dongleIndices : [0]
  const nCover = Math.min(windows.length, indices.length)
  const sources = windows.slice(0, nCover).map((w, i) => ({
    driver: 'osmosdr',
    device: `rtl=${indices[i]}`,
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

  // Cover the full imported frequency span if available, else just the control
  // channels. Spread the coverage across the dongles assigned to trunk-recorder.
  const coverHz = trFrequencies.value.length ? trFrequencies.value : controlChannels
  const { sources, uncovered } = buildTRSources(coverHz, controlChannels, trDongleIndices.value)

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
    msg += ` ${sources.length} SDR source(s) from ${trDongleIndices.value.length || 1} dongle(s).`
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
    assignments[idx] = { ...assignments[idx], assignTo }
  } else {
    assignments.push({ index: dongle.index, serialNumber: dongle.serialNumber, assignTo })
  }
  bridge.value = { ...bridge.value, sdrDeviceAssignments: assignments }
}

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
      squelchDb: -55,
      sampleRate: 8000,
      systemRef: 0,
      talkgroupRef: 0,
      udpPort: maxPort + 1,
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
      squelchDb: -55,
      sampleRate: 8000,
      systemRef: sys.systemRef,
      talkgroupRef: tg.talkgroupRef,
      udpPort: nextFreePort(used),
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

// Usable RF span one RTL-SDR dongle can cover at a 2.4 MHz sample rate, leaving
// margin for filter rolloff and the centre DC spike.
const SDR_USABLE_HZ = 2_000_000

// autoAssignDevices clusters channel frequencies into the fewest ~2 MHz windows
// (one per SDR) so nearby frequencies share a dongle and the available SDRs cover
// as much spectrum as possible. It fills the Dev Set column; the user then Saves
// and Provisions. With 4 dongles and clustered IndyCar pit/track freqs this maps
// everything automatically.
function autoAssignDevices() {
  const withFreq = bridge.value.channels.filter(c => c.frequencyHz > 0)
  if (!withFreq.length) {
    toast.add({ title: 'Add channels with frequencies first', color: 'warning' })
    return
  }
  const sorted = [...withFreq].sort((a, b) => a.frequencyHz - b.frequencyHz)
  // Greedy minimum-windows cover: each window starts at the first freq it can't fit.
  const windowStarts: number[] = [sorted[0].frequencyHz]
  for (const ch of sorted) {
    if (ch.frequencyHz - windowStarts[windowStarts.length - 1] > SDR_USABLE_HZ) {
      windowStarts.push(ch.frequencyHz)
    }
  }
  const needed = windowStarts.length
  // Only the dongles assigned to SDRangel are available to the bridge — the rest
  // are reserved for trunk-recorder. Fall back to all detected dongles when no
  // assignment has been made yet.
  const sdrangelCount = bridge.value.sdrDeviceAssignments.filter(a => a.assignTo === 'sdrangel').length
  const available = sdrangelCount || dongles.value.length || 4

  // Map a frequency to its ~2 MHz window index (the highest window start ≤ freq).
  const windowForFreq = (hz: number) => {
    for (let i = windowStarts.length - 1; i >= 0; i--) {
      if (hz >= windowStarts[i]) return i
    }
    return 0
  }
  // Only hand out device-set indices for SDRs that physically exist. A channel
  // whose window falls beyond the available dongles is parked (deviceSetIndex
  // -1) instead of being pointed at a device set that was never created —
  // provisioning skips it and it simply isn't received until more SDRs are
  // added. Reception within each covered window is unaffected: one dongle
  // handles every channel inside its ~2 MHz window at once.
  const deviceForFreq = (hz: number) => {
    const w = windowForFreq(hz)
    return w < available ? w : -1
  }
  bridge.value = {
    ...bridge.value,
    channels: bridge.value.channels.map(c =>
      c.frequencyHz > 0 ? { ...c, deviceSetIndex: deviceForFreq(c.frequencyHz) } : c),
  }

  const spanMHz = ((sorted[sorted.length - 1].frequencyHz - sorted[0].frequencyHz) / 1e6).toFixed(2)
  if (needed > available) {
    const parked = withFreq.filter(c => windowForFreq(c.frequencyHz) >= available).length
    toast.add({
      title: `Needs ${needed} SDRs, only ${available} available`,
      description: `Frequencies span ${spanMHz} MHz; each SDR covers ~${(SDR_USABLE_HZ / 1e6).toFixed(1)} MHz. Assigned ${available} SDR${available > 1 ? 's' : ''}; ${parked} channel(s) left unassigned (Dev Set −1) — they won't be received until more SDRs are added.`,
      color: 'warning',
    })
  } else {
    toast.add({
      title: `Assigned ${withFreq.length} channel(s) across ${needed} SDR${needed > 1 ? 's' : ''}`,
      description: `Span ${spanMHz} MHz. Review the Dev Set column, Save, then Provision.`,
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
          {{ trDongleIndices.length }} dongle(s) assigned to trunk-recorder
          <span v-if="!trDongleIndices.length" class="text-yellow-500">(assign dongles on the SDR Devices tab for multi-SDR coverage)</span>.
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

      <div v-if="dongles.length" class="rounded border border-neutral-800 overflow-x-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="bg-neutral-900 text-neutral-500">
              <th class="px-3 py-2 text-left">#</th>
              <th class="px-3 py-2 text-left">Manufacturer</th>
              <th class="px-3 py-2 text-left">Product</th>
              <th class="px-3 py-2 text-left">Serial</th>
              <th class="px-3 py-2 text-left">Assign To</th>
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
            </tr>
          </tbody>
        </table>
      </div>

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
        <span class="text-sm font-semibold text-neutral-300">Channels ({{ bridge.channels.length }})</span>
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
        </div>
      </div>

      <div class="rounded border border-neutral-800 overflow-x-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="bg-neutral-900 text-neutral-500">
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
                <UInput v-model.number="ch.deviceSetIndex" type="number" size="xs" />
                <span v-if="ch.deviceSetIndex < 0" class="block text-[10px] text-amber-500 mt-0.5">unassigned</span>
              </td>
              <td class="px-2 py-1">
                <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="xs"
                  @click="removeChannel(i)" />
              </td>
            </tr>
            <tr v-if="!bridge.channels.length">
              <td colspan="9" class="px-3 py-6 text-center text-neutral-600">
                No channels — add one above, then click Provision SDRangel.
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <p class="text-xs text-neutral-600">
        Each channel maps one SDRangel NFM/AM/USB/LSB demodulator to a rdio-scanner talkgroup via UDP audio relay.
        Squelch (dB) is the per-channel threshold SDRangel uses to gate audio — lower it (e.g. −60) if weak signals
        aren't opening, raise it if noise records constantly. After editing, save config and click Provision SDRangel.
      </p>
    </div>
  </div>
</template>
