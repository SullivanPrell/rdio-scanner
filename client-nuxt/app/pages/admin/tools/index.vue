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
const importType = ref<'chirp' | 'rr-csv' | 'trs-csv'>('chirp')
const importFile = ref<File | null>(null)
const importSystemLabel = ref('')
const importSystemRef = ref(1)
const importPortBase = ref(50000)
const importLoading = ref(false)
const importResult = ref<unknown>(null)

const handleFileChange = (e: Event) => {
  const input = e.target as HTMLInputElement
  importFile.value = input.files?.[0] ?? null
}

const runImport = async () => {
  if (!importFile.value) return
  importLoading.value = true
  importResult.value = null

  let res: unknown = null
  if (importType.value === 'chirp') {
    res = await admin.importChirp(importFile.value, importSystemLabel.value, importSystemRef.value, importPortBase.value)
  } else if (importType.value === 'rr-csv') {
    res = await admin.importRRCsv(importFile.value, importSystemLabel.value, importSystemRef.value, importPortBase.value)
  } else if (importType.value === 'trs-csv') {
    res = await admin.importTrsCsv(importFile.value, importSystemLabel.value, importSystemRef.value)
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

        <div class="space-y-4">
          <UFormField label="Import format">
            <URadioGroup
              v-model="importType"
              :options="[
                { label: 'CHIRP (analog/digital frequencies)', value: 'chirp' },
                { label: 'RadioReference Frequency Export', value: 'rr-csv' },
                { label: 'RadioReference Trunked System (TRS)', value: 'trs-csv' },
              ]"
            />
          </UFormField>

          <div class="grid grid-cols-2 gap-4">
            <UFormField label="System label">
              <UInput v-model="importSystemLabel" placeholder="My City P25" />
            </UFormField>
            <UFormField label="System ref (numeric ID)">
              <UInput v-model.number="importSystemRef" type="number" min="1" />
            </UFormField>
          </div>

          <UFormField
            v-if="importType !== 'trs-csv'"
            label="Bridge port base"
            description="Starting UDP port for SDRangel channels (e.g. 50000)"
          >
            <UInput v-model.number="importPortBase" type="number" min="1024" max="65000" />
          </UFormField>

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
              :options="(cfg?.systems ?? []).map(s => ({ label: s.label, value: s.systemRef }))"
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
            After downloading, place <code class="bg-neutral-800 px-1 rounded">trunk-recorder.json</code>
            in <code class="bg-neutral-800 px-1 rounded">/etc/trunk-recorder/</code> and restart the service.
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
