<script setup lang="ts">
import type { DirwatchEntry, AdminSystem } from '~/composables/useAdmin'

const props = defineProps<{
  modelValue: DirwatchEntry[]
  systems: AdminSystem[]
  docker: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [DirwatchEntry[]] }>()

const entries = computed({
  get: () => props.modelValue ?? [],
  set: v => emit('update:modelValue', v),
})

const systemOptions = computed(() =>
  (props.systems ?? []).map(s => ({ label: s.label, value: s.id }))
)

const typeOptions = [
  { label: 'Default', value: 'default' },
  { label: 'DSDPlus', value: 'dsdplus' },
  { label: 'SDR-Trunk', value: 'sdr-trunk' },
  { label: 'Trunk Recorder', value: 'trunk-recorder' },
]

const add = () => {
  entries.value = [...entries.value, {
    directory: '',
    disabled: false,
    type: 'default',
    mask: '#SYS-#TG-#DATE-#TIME.wav',
  }]
}
</script>

<template>
  <div class="space-y-3">
    <UAlert v-if="docker" color="warning" icon="i-heroicons-exclamation-triangle">
      Dir watch is not available in Docker mode.
    </UAlert>

    <div v-if="!docker">
      <div class="flex justify-end mb-3">
        <UButton icon="i-heroicons-plus" size="sm" variant="soft" @click="add">Add Watch</UButton>
      </div>

      <UAccordion
        v-for="(entry, i) in entries"
        :key="i"
        :items="[{ label: entry.directory || 'New Dir Watch', slot: 'dw-' + i }]"
        class="border border-neutral-800 rounded-lg mb-2"
      >
        <template #[`dw-${i}`]>
          <div class="p-4 space-y-3">
            <div class="grid grid-cols-2 gap-3">
              <UFormField label="Directory" class="col-span-2">
                <UInput v-model="entry.directory" placeholder="/path/to/audio/files" />
              </UFormField>
              <UFormField label="Type">
                <USelect v-model="entry.type" :options="typeOptions" />
              </UFormField>
              <UFormField label="System">
                <USelect v-model="entry.systemId" :options="systemOptions" placeholder="Any" />
              </UFormField>
              <UFormField label="Extension">
                <UInput v-model="entry.extension" placeholder=".wav, .mp3" />
              </UFormField>
              <UFormField v-if="['default'].includes(entry.type)" label="Frequency (Hz)">
                <UInput v-model.number="entry.frequency" type="number" />
              </UFormField>
            </div>

            <UFormField v-if="entry.type !== 'trunk-recorder'" label="File mask">
              <UInput v-model="entry.mask" placeholder="#SYS-#TG-#DATE-#TIME.wav" />
            </UFormField>

            <div class="flex items-center gap-4">
              <UCheckbox v-model="entry.disabled" label="Disabled" />
              <UCheckbox v-model="entry.deleteAfter" label="Delete file after ingest" />
            </div>

            <div class="flex justify-end">
              <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="sm"
                @click="entries.value = entries.value.filter((_, idx) => idx !== i)">
                Remove
              </UButton>
            </div>
          </div>
        </template>
      </UAccordion>

      <div v-if="!entries.length" class="text-center text-neutral-500 py-8 text-sm">
        No dir watches configured.
      </div>
    </div>
  </div>
</template>
