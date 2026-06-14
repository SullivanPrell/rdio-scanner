<script setup lang="ts">
import type { Options } from '~/composables/useAdmin'

const props = defineProps<{ modelValue: Options }>()
const emit = defineEmits<{ 'update:modelValue': [Options] }>()
const opts = computed({ get: () => props.modelValue, set: v => emit('update:modelValue', v) })

const audioConversionOptions = [
  { label: 'None (keep original)', value: '' },
  { label: 'MP3', value: 'mp3' },
  { label: 'Opus', value: 'opus' },
  { label: 'AAC', value: 'aac' },
]

const beepOptions = [
  { label: 'Off', value: 'off' },
  { label: 'Chirps', value: 'chirps' },
  { label: 'Beep', value: 'beep' },
]
</script>

<template>
  <div class="space-y-6 max-w-2xl">
    <div class="grid grid-cols-2 gap-4">
      <UFormField label="Max simultaneous clients">
        <UInput v-model.number="opts.maxClients" type="number" min="1" />
      </UFormField>

      <UFormField label="Prune calls older than (days)">
        <UInput v-model.number="opts.pruneDays" type="number" min="0" />
      </UFormField>

      <UFormField label="Audio conversion">
        <USelect v-model="opts.audioConversion" :options="audioConversionOptions" />
      </UFormField>

      <UFormField label="Keypad beeps">
        <USelect v-model="opts.keypadBeeps" :options="beepOptions" />
      </UFormField>

      <UFormField label="Dimmer delay (s, 0 = off)">
        <UInput v-model.number="opts.dimmerDelay" type="number" min="0" />
      </UFormField>

      <UFormField label="Duplicate call suppress (s)">
        <UInput v-model.number="opts.duplicatesDelay" type="number" min="0" />
      </UFormField>

      <UFormField label="Branding text" class="col-span-2">
        <UInput v-model="opts.branding" placeholder="My Radio Scanner" />
      </UFormField>
    </div>

    <div class="space-y-2">
      <UCheckbox v-model="opts.autoPopulate" label="Auto-populate unknown talkgroups" />
      <UCheckbox v-model="opts.playbackGoesLive" label="Playback goes live after queue empties" />
      <UCheckbox v-model="opts.showListenersCount" label="Show listeners count" />
      <UCheckbox v-model="opts.sortTalkgroups" label="Sort talkgroups" />
      <UCheckbox v-model="opts.tagsToggle" label="Enable tags toggle" />
      <UCheckbox v-model="opts.time12hFormat" label="12-hour time format" />
      <UCheckbox v-model="opts.searchPatchedTalkgroups" label="Search patched talkgroups" />
    </div>
  </div>
</template>
