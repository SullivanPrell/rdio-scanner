<script setup lang="ts">
import type { BridgeConfig, AdminSystem } from '~/composables/useAdmin'

const props = defineProps<{ modelValue: BridgeConfig; systems: AdminSystem[] }>()
const emit = defineEmits<{ 'update:modelValue': [BridgeConfig] }>()
const bridge = computed({ get: () => props.modelValue, set: v => emit('update:modelValue', v) })

const admin = useAdmin()
const toast = useToast()
const provisioning = ref(false)

const systemOptions = computed(() => props.systems.map(s => ({ label: s.label, value: s.systemRef })))

const talkgroupOptions = (systemRef: number) => {
  const sys = props.systems.find(s => s.systemRef === systemRef)
  return sys?.talkgroups.map(tg => ({ label: `${tg.talkgroupRef} – ${tg.name || tg.label}`, value: tg.talkgroupRef })) ?? []
}

const protocolOptions = [
  { label: 'NFM (analog)', value: 'nfm' },
  { label: 'DSD (digital)', value: 'dsd' },
  { label: 'NXDN', value: 'nxdn' },
]

const addChannel = () => {
  const maxPort = bridge.value.channels.reduce((m, c) => Math.max(m, c.udpPort), 50000)
  bridge.value.channels.push({
    channelIndex: 0,
    deviceSetIndex: 0,
    frequencyHz: 0,
    label: '',
    protocol: 'nfm',
    sampleRate: 8000,
    systemRef: 0,
    talkgroupRef: 0,
    udpPort: maxPort + 1,
  })
}

const provision = async () => {
  provisioning.value = true
  const deviceSets = bridge.value.channels.reduce((acc, ch) => {
    if (!acc.find((d: { index: number }) => d.index === ch.deviceSetIndex)) {
      acc.push({ index: ch.deviceSetIndex, hwType: 'RTLSDR', sequence: ch.deviceSetIndex, centerFrequencyHz: ch.frequencyHz, sampleRateHz: 2400000 })
    }
    return acc
  }, [] as unknown[])

  const result = await admin.provisionSDRangel({ deviceSets, channels: bridge.value.channels })
  provisioning.value = false
  if (result) toast.add({ title: 'SDRangel provisioned', color: 'success' })
}
</script>

<template>
  <div class="space-y-4">
    <!-- Global bridge settings -->
    <div class="grid grid-cols-3 gap-3">
      <UFormField label="Host">
        <UInput v-model="bridge.host" placeholder="127.0.0.1" />
      </UFormField>
      <UFormField label="Port">
        <UInput v-model.number="bridge.port" type="number" placeholder="8091" />
      </UFormField>
      <UFormField label="">
        <div class="flex items-center gap-3 mt-6">
          <UCheckbox v-model="bridge.enabled" label="Enabled" />
        </div>
      </UFormField>
    </div>

    <!-- Channels table -->
    <div>
      <div class="flex items-center justify-between mb-2">
        <span class="text-sm font-semibold text-neutral-300">Channels ({{ bridge.channels.length }})</span>
        <div class="flex gap-2">
          <UButton icon="i-heroicons-plus" size="xs" variant="ghost" @click="addChannel">Add Channel</UButton>
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
              <td class="px-2 py-1"><USelect v-model="ch.protocol" :options="protocolOptions" size="xs" /></td>
              <td class="px-2 py-1"><USelect v-model.number="ch.systemRef" :options="systemOptions" size="xs" /></td>
              <td class="px-2 py-1">
                <USelect v-model.number="ch.talkgroupRef" :options="talkgroupOptions(ch.systemRef)" size="xs" />
              </td>
              <td class="px-2 py-1"><UInput v-model.number="ch.udpPort" type="number" size="xs" /></td>
              <td class="px-2 py-1"><UInput v-model.number="ch.deviceSetIndex" type="number" size="xs" /></td>
              <td class="px-2 py-1">
                <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="xs"
                  @click="bridge.channels.splice(i, 1)" />
              </td>
            </tr>
            <tr v-if="!bridge.channels.length">
              <td colspan="8" class="px-3 py-4 text-center text-neutral-600">
                No channels — import from CSV or add manually.
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
