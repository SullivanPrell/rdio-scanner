<script setup lang="ts">
import type { AccessConfig, AdminSystem } from '~/composables/useAdmin'
const props = defineProps<{ modelValue: AccessConfig[]; systems: AdminSystem[] }>()
const emit = defineEmits<{ 'update:modelValue': [AccessConfig[]] }>()
const entries = computed({ get: () => props.modelValue, set: v => emit('update:modelValue', v) })
const add = () => entries.value = [...entries.value, { code: '', ident: '', systems: {} }]
</script>
<template>
  <div class="space-y-3">
    <div class="flex justify-end">
      <UButton icon="i-heroicons-plus" size="sm" variant="soft" @click="add">Add Access</UButton>
    </div>
    <div v-if="!entries.length" class="text-center text-neutral-500 py-8 text-sm">No access codes configured — leave empty for open access.</div>
    <div v-for="(entry, i) in entries" :key="i" class="border border-neutral-800 rounded-lg p-4 space-y-3">
      <div class="grid grid-cols-2 gap-3">
        <UFormField label="Ident"><UInput v-model="entry.ident" /></UFormField>
        <UFormField label="Code"><UInput v-model="entry.code" /></UFormField>
        <UFormField label="Expiration (optional)"><UInput v-model="entry.expiration" type="datetime-local" /></UFormField>
        <UFormField label="Limit (0 = unlimited)"><UInput v-model.number="entry.limit" type="number" min="0" /></UFormField>
      </div>
      <div class="flex justify-end">
        <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="sm" @click="entries.value = entries.value.filter((_,idx)=>idx!==i)">Remove</UButton>
      </div>
    </div>
  </div>
</template>
