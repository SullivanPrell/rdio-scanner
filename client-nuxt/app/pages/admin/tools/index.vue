<script setup lang="ts">
import type { AdminConfig } from '~/composables/useAdmin'

definePageMeta({ layout: 'admin' })

const admin = useAdmin()
const toast = useToast()

const cfg = ref<AdminConfig | null>(null)
const activeTool = ref('import')

const tools = [
  { key: 'import', label: 'CSV Import', icon: 'i-heroicons-arrow-up-tray' },
  { key: 'radioreference', label: 'RadioReference', icon: 'i-heroicons-globe-americas' },
  { key: 'sdrangel', label: 'SDRangel', icon: 'i-heroicons-signal' },
  { key: 'trunkrecorder', label: 'Trunk Recorder', icon: 'i-heroicons-wifi' },
  { key: 'password', label: 'Password', icon: 'i-heroicons-lock-closed' },
]

onMounted(async () => {
  cfg.value = await admin.getConfig()
})

// ── CSV Import ─────────────────────────────────────────────────────────────
const importFormat = ref<'chirp' | 'rr'>('chirp')
const importUsage = ref<'standard' | 'trunk'>('standard')
const importProtocol = ref('')
const importFile = ref<File | null>(null)
const importSystemLabel = ref('')
const importSystemRef = ref(1)
const importPortBase = ref(50000)
const importLoading = ref(false)
const importResult = ref<unknown>(null)

const standardProtocolOptions = [
  { label: 'Auto-detect from CSV', value: 'auto' },
  { label: 'NFM (narrowband FM)', value: 'nfm' },
  { label: 'AM', value: 'am' },
  { label: 'P25', value: 'p25' },
  { label: 'DSD (P25 / DMR / NXDN auto)', value: 'dsd' },
]
const trunkKindOptions = [
  { label: 'P25 Phase 1', value: 'p25' },
  { label: 'P25 Phase 2', value: 'p25phase2' },
  { label: 'DMR', value: 'dmr' },
  { label: 'NXDN', value: 'nxdn' },
]

const typeOptions = computed(() =>
  importUsage.value === 'trunk' ? trunkKindOptions : standardProtocolOptions,
)

// Reka rejects a select item whose value is the empty string, so "auto-detect"
// is the 'auto' sentinel in the dropdown while importProtocol stays '' on the
// wire (what the import endpoints expect for auto-detect).
const importProtocolModel = computed({
  get: () => importProtocol.value || 'auto',
  set: (v: string) => { importProtocol.value = v === 'auto' ? '' : v },
})

watch(importFormat, fmt => {
  if (fmt === 'chirp') importUsage.value = 'standard'
})
watch(importUsage, () => {
  importProtocol.value = importUsage.value === 'trunk' ? 'p25' : ''
})

const handleFileChange = (e: Event) => {
  const input = e.target as HTMLInputElement
  importFile.value = input.files?.[0] ?? null
}

const runImport = async () => {
  if (!importFile.value) return
  importLoading.value = true
  importResult.value = null

  let res: unknown = null
  if (importFormat.value === 'chirp') {
    res = await admin.importChirp(importFile.value, importSystemLabel.value, importSystemRef.value, importPortBase.value, importProtocol.value)
  } else if (importUsage.value === 'standard') {
    res = await admin.importRRCsv(importFile.value, importSystemLabel.value, importSystemRef.value, importPortBase.value, importProtocol.value)
  } else {
    res = await admin.importTrsCsv(importFile.value, importSystemLabel.value, importSystemRef.value, importProtocol.value)
  }

  importResult.value = res
  importLoading.value = false

  if (res) {
    toast.add({ title: 'Import complete', description: 'Config updated — review and save.', color: 'success' })
    cfg.value = await admin.getConfig()
  }
}

// ── SDRangel ────────────────────────────────────────────────────────────────
const sdrangelStatus = ref<Awaited<ReturnType<typeof admin.getSDRangelStatus>> | null>(null)
const dongles = ref<Awaited<ReturnType<typeof admin.getDongles>>>([])
const sdrangelLoading = ref(false)

const refreshSDRangel = async () => {
  sdrangelLoading.value = true
  ;[sdrangelStatus.value, dongles.value] = await Promise.all([
    admin.getSDRangelStatus(),
    admin.getDongles(),
  ])
  sdrangelLoading.value = false
}

// ── Trunk-recorder config gen ──────────────────────────────────────────────
const trSystemRef = ref(1)
const trControlChannels = ref('')
const trSystemType = ref('P25')
const trLoading = ref(false)
const trResult = ref<unknown>(null)

const generateTRConfig = async () => {
  const channels = trControlChannels.value
    .split(/[\s,]+/)
    .map(s => parseInt(s.replace(/[^0-9]/g, ''), 10))
    .filter(n => n > 0)

  if (!channels.length) {
    toast.add({ title: 'Enter at least one control channel frequency', color: 'warning' })
    return
  }

  trLoading.value = true
  const apiKey = cfg.value?.apikeys?.[0]?.key ?? ''

  trResult.value = await admin.generateTrunkRecorderConfig({
    systemRef: trSystemRef.value,
    controlChannels: channels,
    systemType: trSystemType.value,
    apiKey,
  })
  trLoading.value = false

  if (trResult.value) {
    // Trigger download
    const blob = new Blob([JSON.stringify(trResult.value, null, 2)], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'trunk-recorder.json'
    a.click()
    URL.revokeObjectURL(a.href)
    toast.add({ title: 'trunk-recorder.json downloaded', color: 'success' })
  }
}

// ── Password change ────────────────────────────────────────────────────────
const pwCurrent = ref('')
const pwNew = ref('')
const pwConfirm = ref('')
const pwLoading = ref(false)

const changePassword = async () => {
  if (pwNew.value !== pwConfirm.value) {
    toast.add({ title: 'Passwords do not match', color: 'error' })
    return
  }
  pwLoading.value = true
  const ok = await admin.changePassword(pwCurrent.value, pwNew.value)
  if (ok) {
    pwCurrent.value = ''
    pwNew.value = ''
    pwConfirm.value = ''
  }
  pwLoading.value = false
}

useHead({ title: 'Tools – Admin – Rdio Scanner' })
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-xl font-bold">Tools</h1>

    <!-- Tool tabs -->
    <div class="flex flex-wrap gap-1 border-b border-neutral-800 pb-2">
      <button
        v-for="tool in tools"
        :key="tool.key"
        class="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm transition-colors"
        :class="activeTool === tool.key
          ? 'bg-neutral-800 text-white'
          : 'text-neutral-400 hover:text-white hover:bg-neutral-900'"
        @click="activeTool = tool.key"
      >
        <UIcon :name="tool.icon" class="size-3.5" />
        {{ tool.label }}
      </button>
    </div>

    <!-- ── CSV Import ──────────────────────────────────────────────────── -->
    <template v-if="activeTool === 'import'">
      <UCard>
        <template #header><span class="font-semibold">CSV Import</span></template>

        <div class="space-y-5">
          <!-- Step 1: Format -->
          <UFormField label="Format">
            <div class="flex gap-2 flex-wrap">
              <button
                v-for="opt in [
                  { value: 'chirp', label: 'CHIRP', desc: 'Analog / digital frequencies' },
                  { value: 'rr',    label: 'RadioReference', desc: 'Frequency or TRS export' },
                ]"
                :key="opt.value"
                type="button"
                class="flex-1 min-w-32 px-4 py-2.5 rounded-lg border text-left transition-colors"
                :class="importFormat === opt.value
                  ? 'border-primary-500 bg-primary-500/10 text-white'
                  : 'border-neutral-700 text-neutral-400 hover:border-neutral-500'"
                @click="importFormat = opt.value as 'chirp' | 'rr'"
              >
                <div class="font-medium text-sm">{{ opt.label }}</div>
                <div class="text-xs opacity-70 mt-0.5">{{ opt.desc }}</div>
              </button>
            </div>
          </UFormField>

          <!-- Step 2: Import type -->
          <UFormField label="Import type">
            <div class="flex gap-2 flex-wrap">
              <button
                v-for="opt in [
                  { value: 'standard', label: 'Standard', desc: 'SDRangel bridge channels' },
                  { value: 'trunk',    label: 'Trunk', desc: 'trunk-recorder talkgroups' },
                ]"
                :key="opt.value"
                type="button"
                class="flex-1 min-w-32 px-4 py-2.5 rounded-lg border text-left transition-colors"
                :class="[
                  importUsage === opt.value
                    ? 'border-primary-500 bg-primary-500/10 text-white'
                    : 'border-neutral-700 text-neutral-400 hover:border-neutral-500',
                  opt.value === 'trunk' && importFormat === 'chirp'
                    ? 'opacity-40 cursor-not-allowed pointer-events-none'
                    : '',
                ]"
                :disabled="opt.value === 'trunk' && importFormat === 'chirp'"
                @click="importUsage = opt.value as 'standard' | 'trunk'"
              >
                <div class="font-medium text-sm">{{ opt.label }}</div>
                <div class="text-xs opacity-70 mt-0.5">{{ opt.desc }}</div>
              </button>
            </div>
            <p v-if="importFormat === 'chirp'" class="text-xs text-neutral-500 mt-1.5">
              CHIRP exports conventional frequencies — Trunk mode requires a RadioReference TRS export.
            </p>
          </UFormField>

          <!-- Step 3: Type / Protocol -->
          <UFormField
            :label="importUsage === 'trunk' ? 'System type' : 'Protocol'"
            :description="importUsage === 'trunk'
              ? 'Trunking protocol of this system'
              : 'Channel modulation — Auto-detect reads the mode from the CSV'"
          >
            <USelect v-model="importProtocolModel" :items="typeOptions" class="max-w-xs" />
          </UFormField>

          <!-- System label & ref -->
          <div class="grid grid-cols-2 gap-4">
            <UFormField label="System label">
              <UInput v-model="importSystemLabel" placeholder="My City P25" />
            </UFormField>
            <UFormField label="System ref (numeric ID)">
              <UInput v-model.number="importSystemRef" type="number" min="1" />
            </UFormField>
          </div>

          <!-- Port base (Standard only) -->
          <UFormField
            v-if="importUsage === 'standard'"
            label="Bridge port base"
            description="Starting UDP port for SDRangel channels (e.g. 50000)"
          >
            <UInput v-model.number="importPortBase" type="number" min="1024" max="65000" class="max-w-xs" />
          </UFormField>

          <!-- File picker -->
          <UFormField label="CSV file">
            <input type="file" accept=".csv,.txt" @change="handleFileChange" />
          </UFormField>

          <UButton
            icon="i-heroicons-arrow-up-tray"
            :loading="importLoading"
            :disabled="!importFile"
            @click="runImport"
          >
            Import
          </UButton>

          <UAlert
            v-if="importResult"
            color="success"
            icon="i-heroicons-check-circle"
            title="Import complete"
            description="Systems and talkgroups have been merged into your config. Go to Config → Systems to review, then Save."
          />
        </div>
      </UCard>
    </template>

    <!-- ── RadioReference API ─────────────────────────────────────────── -->
    <template v-else-if="activeTool === 'radioreference'">
      <AdminToolsRadioReference :systems="cfg?.systems ?? []" />
    </template>

    <!-- ── SDRangel setup ─────────────────────────────────────────────── -->
    <template v-else-if="activeTool === 'sdrangel'">
      <UCard>
        <template #header>
          <div class="flex items-center justify-between">
            <span class="font-semibold">SDRangel Status</span>
            <UButton size="xs" variant="ghost" icon="i-heroicons-arrow-path" :loading="sdrangelLoading" @click="refreshSDRangel">
              Refresh
            </UButton>
          </div>
        </template>

        <div v-if="!sdrangelStatus" class="text-neutral-500 text-sm">
          Click Refresh to check SDRangel and detect dongles.
        </div>

        <template v-else>
          <div class="flex items-center gap-2 mb-4">
            <span class="led" :class="{ active: sdrangelStatus.connected }" />
            <span class="text-sm">
              {{ sdrangelStatus.connected ? `Connected — SDRangel ${sdrangelStatus.version}` : 'Not connected' }}
            </span>
          </div>

          <!-- Dongles -->
          <div v-if="dongles.length" class="mb-4">
            <div class="text-xs text-neutral-500 uppercase tracking-wide mb-2">Detected RTL-SDR Dongles</div>
            <div class="space-y-1">
              <div v-for="d in dongles" :key="d.index" class="flex items-center gap-2 p-2 rounded bg-neutral-900 text-sm">
                <UIcon name="i-heroicons-cpu-chip" class="size-4 text-neutral-400" />
                <span class="font-mono text-neutral-300">#{{ d.index }}</span>
                <span>{{ d.manufacturer }} {{ d.product }}</span>
                <span class="text-neutral-500 text-xs ml-auto font-mono">SN:{{ d.serialNumber }}</span>
              </div>
            </div>
          </div>
          <div v-else class="text-sm text-neutral-500 mb-4">No RTL-SDR dongles detected.</div>

          <!-- Device sets -->
          <div v-if="sdrangelStatus.deviceSets?.length">
            <div class="text-xs text-neutral-500 uppercase tracking-wide mb-2">Device Sets</div>
            <div class="space-y-2">
              <UAccordion
                v-for="ds in sdrangelStatus.deviceSets"
                :key="ds.index"
                :items="[{ label: `Device Set ${ds.index} — ${ds.hwType}`, content: ds }]"
              />
            </div>
          </div>
        </template>

        <template #footer>
          <div class="text-xs text-neutral-500">
            Configure Bridge channels in Config → Bridge, then click Provision to push settings to SDRangel.
          </div>
        </template>
      </UCard>
    </template>

    <!-- ── Trunk-recorder config ──────────────────────────────────────── -->
    <template v-else-if="activeTool === 'trunkrecorder'">
      <UCard>
        <template #header><span class="font-semibold">Generate trunk-recorder.json</span></template>

        <div class="space-y-4">
          <p class="text-sm text-neutral-400">
            Select a trunked system and enter its control channel frequencies.
            A ready-to-use <code class="bg-neutral-800 px-1 rounded">trunk-recorder.json</code> will be generated and downloaded.
          </p>

          <UFormField label="System">
            <USelect
              v-model.number="trSystemRef"
              :items="(cfg?.systems ?? []).map(s => ({ label: s.label, value: s.systemRef }))"
              placeholder="Select system"
            />
          </UFormField>

          <UFormField
            label="System type"
            description="P25, MPT1327, DMR, NXDN96, etc."
          >
            <UInput v-model="trSystemType" placeholder="P25" />
          </UFormField>

          <UFormField
            label="Control channel frequencies (Hz)"
            description="Comma or space separated. Example: 851012500, 851287500"
          >
            <UTextarea v-model="trControlChannels" rows="3" placeholder="851012500, 851287500" />
          </UFormField>

          <UButton
            icon="i-heroicons-arrow-down-tray"
            :loading="trLoading"
            @click="generateTRConfig"
          >
            Generate &amp; Download
          </UButton>
        </div>

        <template #footer>
          <div class="text-xs text-neutral-500">
            This downloads a copy for inspection. To generate and start trunk-recorder
            on this Pi, use Config → Bridge → Trunk Recorder — it saves the config
            automatically, no path needed.
          </div>
        </template>
      </UCard>
    </template>

    <!-- ── Password ───────────────────────────────────────────────────── -->
    <template v-else-if="activeTool === 'password'">
      <UCard class="max-w-md">
        <template #header><span class="font-semibold">Change Admin Password</span></template>

        <div class="space-y-3">
          <UFormField label="Current password">
            <UInput v-model="pwCurrent" type="password" />
          </UFormField>
          <UFormField label="New password">
            <UInput v-model="pwNew" type="password" />
          </UFormField>
          <UFormField label="Confirm new password">
            <UInput v-model="pwConfirm" type="password" />
          </UFormField>
          <UButton :loading="pwLoading" @click="changePassword">Change Password</UButton>
        </div>
      </UCard>
    </template>
  </div>
</template>
