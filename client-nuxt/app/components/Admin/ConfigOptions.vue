<script setup lang="ts">
import type { Options } from '~/composables/useAdmin'

const props = defineProps<{ modelValue: Options }>()
const emit = defineEmits<{ 'update:modelValue': [Options] }>()
const opts = computed({ get: () => props.modelValue, set: v => emit('update:modelValue', v) })

// Matches the server AudioConversion enum (options.go): 0=off, 1=on, 2=+normalize, 3=+loudnorm
const audioConversionOptions = [
  { label: 'Disabled', value: 0 },
  { label: 'Enabled', value: 1 },
  { label: 'Enabled + normalize', value: 2 },
  { label: 'Enabled + loudnorm', value: 3 },
]

const beepOptions = [
  { label: 'Uniden', value: 'uniden' },
  { label: 'Whistler', value: 'whistler' },
  { label: 'Off', value: 'off' },
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
        <USelect v-model.number="opts.audioConversion" :items="audioConversionOptions" />
      </UFormField>

      <UFormField label="Keypad beeps">
        <USelect v-model="opts.keypadBeeps" :items="beepOptions" />
      </UFormField>

      <UFormField label="Dimmer delay (ms, 0 = off)">
        <UInput v-model.number="opts.dimmerDelay" type="number" min="0" />
      </UFormField>

      <UFormField label="Duplicate-call window (ms)" description="Calls within this window are treated as duplicates">
        <UInput v-model.number="opts.duplicateDetectionTimeFrame" type="number" min="0" />
      </UFormField>

      <UFormField label="Branding text" class="col-span-2">
        <UInput v-model="opts.branding" placeholder="My Radio Scanner" />
      </UFormField>

      <UFormField label="Contact email" class="col-span-2">
        <UInput v-model="opts.email" type="email" placeholder="admin@example.com" />
      </UFormField>
    </div>

    <div class="space-y-2">
      <UCheckbox v-model="opts.autoPopulate" label="Auto-populate unknown talkgroups" />
      <UCheckbox v-model="opts.disableDuplicateDetection" label="Disable duplicate-call detection" />
      <UCheckbox v-model="opts.playbackGoesLive" label="Playback goes live after queue empties" />
      <UCheckbox v-model="opts.showListenersCount" label="Show listeners count" />
      <UCheckbox v-model="opts.sortTalkgroups" label="Sort talkgroups" />
      <UCheckbox v-model="opts.time12hFormat" label="12-hour time format" />
    </div>
  </div>
</template>
