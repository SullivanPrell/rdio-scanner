<script setup lang="ts">
import type { ApiKey, AdminSystem } from '~/composables/useAdmin'

const props = defineProps<{ modelValue: ApiKey[]; systems: AdminSystem[] }>()
const emit = defineEmits<{ 'update:modelValue': [ApiKey[]] }>()

const keys = computed({ get: () => props.modelValue ?? [], set: v => emit('update:modelValue', v) })

const newKey = () => crypto.randomUUID?.() ?? Math.random().toString(36).slice(2)

const add = () => {
  keys.value = [...keys.value, { ident: '', key: newKey(), disabled: false, systems: {} }]
}

const copyKey = (key: string) => {
  navigator.clipboard.writeText(key)
    .catch(() => { /* no clipboard in non-secure context */ })
}
</script>

<template>
  <div class="space-y-3">
    <div class="flex justify-end">
      <UButton icon="i-heroicons-plus" size="sm" variant="soft" @click="add">Add API Key</UButton>
    </div>

    <div v-if="!keys.length" class="text-center text-neutral-500 py-8 text-sm">
      No API keys configured.
    </div>

    <div
      v-for="(apikey, i) in keys"
      :key="i"
      class="border border-neutral-800 rounded-lg p-4 space-y-3"
    >
      <div class="grid grid-cols-2 gap-3">
        <UFormField label="Identifier">
          <UInput v-model="apikey.ident" placeholder="My recorder" />
        </UFormField>
        <UFormField label="Key">
          <div class="flex gap-2">
            <UInput v-model="apikey.key" class="flex-1 font-mono text-xs" readonly />
            <UButton icon="i-heroicons-clipboard" variant="ghost" size="sm" @click="copyKey(apikey.key)" />
            <UButton icon="i-heroicons-arrow-path" variant="ghost" size="sm" @click="apikey.key = newKey()" />
          </div>
        </UFormField>
      </div>

      <div class="flex items-center justify-between">
        <UCheckbox v-model="apikey.disabled" label="Disabled" />
        <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="sm"
          @click="keys = keys.filter((_, idx) => idx !== i)">
          Remove
        </UButton>
      </div>
    </div>
  </div>
</template>
