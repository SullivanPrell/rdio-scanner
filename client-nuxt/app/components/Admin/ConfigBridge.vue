<script setup lang="ts">
import type {
  BridgeConfig,
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

// ── Trunk-recorder config generation ─────────────────────────────────────────
const trGenSystemRef = ref<number>(0)
const trGenControlChannels = ref('')
const trGenSystemType = ref('P25')
const trGenerating = ref(false)
const trGenMessage = ref('')

// ── Polling ───────────────────────────────────────────────────────────────────
let pollTimer: ReturnType<typeof setInterval> | null = null

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
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})

watch(subTab, async (tab) => {
  if (tab === 'sdrangel') await refreshSDRangelLogs()
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
    if (!ch.frequencyHz) continue
    const arr = freqsByDevice.get(ch.deviceSetIndex) ?? []
    arr.push(ch.frequencyHz)
    freqsByDevice.set(ch.deviceSetIndex, arr)
  }

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
    return { index, hwType: 'RTLSDR', sequence: index, centerFrequencyHz: center, sampleRateHz: SDR_SAMPLE_RATE }
  })

  const result = await admin.provisionSDRangel({ deviceSets, channels: bridge.value.channels })
  provisioning.value = false
  if (result) toast.add({ title: 'SDRangel provisioned', color: 'success' })
}

// ── Trunk-recorder actions ────────────────────────────────────────────────────
async function trAction(action: 'start' | 'stop' | 'restart') {
  trActioning.value = true
  const result = await admin.trServiceAction(
    action,
    bridge.value.trunkRecorderBinaryPath || undefined,
    bridge.value.trunkRecorderConfigPath || undefined,
  )
  trActioning.value = false
  toast.add({
    title: result.success ? `trunk-recorder ${action}ed` : `Failed to ${action} trunk-recorder`,
    description: result.message,
    color: result.success ? 'success' : 'error',
  })
  await refreshAll()
  await refreshTRLogs()
}

async function generateTRConfig() {
  if (!trGenSystemRef.value) {
    toast.add({ title: 'Select a system first', color: 'warning' })
    return
  }
  const freqs = trGenControlChannels.value
    .split(',')
    .map(s => s.trim())
    .filter(Boolean)
    .map(s => {
      const n = parseFloat(s)
      return n < 1e6 ? Math.round(n * 1e6) : Math.round(n)
    })
  if (!freqs.length) {
    toast.add({ title: 'Enter at least one control channel frequency', color: 'warning' })
    return
  }

  trGenerating.value = true
  trGenMessage.value = ''
  const result = await admin.generateTrunkRecorderConfig({
    systemRef: trGenSystemRef.value,
    controlChannels: freqs,
    systemType: trGenSystemType.value,
    configPath: bridge.value.trunkRecorderConfigPath || undefined,
  })
  trGenerating.value = false

  if (result) {
    trGenMessage.value = result.saveMessage ?? 'Config generated (no save path set — configure Config File Path to save on server)'
    toast.add({ title: 'Config generated', description: trGenMessage.value, color: 'success' })
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

// Precompute talkgroup options per system once, as stable array references.
// The per-row `:items="talkgroupOptions(ch.systemRef)"` binding is re-evaluated
// on every render; returning a fresh array each time made the Reka select
// re-diff all of its options repeatedly while opening, which is super-linear —
// with a few hundred talkgroups, opening one dropdown blocked the main thread
// for seconds (the "browser crash"). A computed Map keeps the reference stable
// so the select only builds its list once.
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
      squelchDb: -50,
      sampleRate: 8000,
      systemRef: 0,
      talkgroupRef: 0,
      udpPort: maxPort + 1,
    }],
  }
}

function removeChannel(i: number) {
  const channels = [...bridge.value.channels]
  channels.splice(i, 1)
  bridge.value = { ...bridge.value, channels }
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
  const deviceForFreq = (hz: number) => {
    for (let i = windowStarts.length - 1; i >= 0; i--) {
      if (hz >= windowStarts[i]) return i
    }
    return 0
  }
  bridge.value = {
    ...bridge.value,
    channels: bridge.value.channels.map(c =>
      c.frequencyHz > 0 ? { ...c, deviceSetIndex: deviceForFreq(c.frequencyHz) } : c),
  }
  const needed = windowStarts.length
  const available = dongles.value.length || 4
  const spanMHz = ((sorted[sorted.length - 1].frequencyHz - sorted[0].frequencyHz) / 1e6).toFixed(2)
  if (needed > available) {
    toast.add({
      title: `Needs ${needed} SDRs, only ${available} available`,
      description: `Frequencies span ${spanMHz} MHz; each SDR covers ~${(SDR_USABLE_HZ / 1e6).toFixed(1)} MHz. Channels on device sets ≥ ${available} won't be received until more SDRs are added.`,
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
          <div v-if="!sdrangelLogs.length" class="text-neutral-600">No log output captured.</div>
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

      <!-- Process settings -->
      <div>
        <p class="text-xs font-semibold text-neutral-400 uppercase tracking-wide mb-2">Process</p>
        <div class="grid grid-cols-2 gap-3">
          <UFormField label="Binary Path" description="Path to trunk-recorder binary (native, recommended)">
            <UInput v-model="bridge.trunkRecorderBinaryPath" placeholder="/usr/local/bin/trunk-recorder" />
          </UFormField>
          <UFormField label="Container Name" description="Docker container name (if using Docker)">
            <UInput v-model="bridge.trunkRecorderContainerName" placeholder="trunk-recorder" />
          </UFormField>
        </div>
        <div class="mt-3">
          <UFormField label="Config File Path" description="Where trunk-recorder.json lives on this Pi (used for Start and Generate)">
            <UInput v-model="bridge.trunkRecorderConfigPath" placeholder="/etc/trunk-recorder/trunk-recorder.json" class="font-mono text-sm" />
          </UFormField>
        </div>
      </div>

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
                  :model-value="dongleAssignment(dongle.index)"
                  size="xs"
                  :items="[
                    { label: 'Unassigned', value: '' },
                    { label: 'SDRangel', value: 'sdrangel' },
                    { label: 'Trunk-Recorder', value: 'trunk-recorder' },
                  ]"
                  @update:model-value="v => setDongleAssignment(dongle, v as '' | 'sdrangel' | 'trunk-recorder')"
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
        <div class="flex gap-2">
          <UButton icon="i-heroicons-plus" size="xs" variant="ghost" @click="addChannel">
            Add Channel
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
                <USelect v-model.number="ch.talkgroupRef" :items="talkgroupOptions(ch.systemRef)" size="xs" />
              </td>
              <td class="px-2 py-1"><UInput v-model.number="ch.udpPort" type="number" size="xs" /></td>
              <td class="px-2 py-1"><UInput v-model.number="ch.deviceSetIndex" type="number" size="xs" /></td>
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
