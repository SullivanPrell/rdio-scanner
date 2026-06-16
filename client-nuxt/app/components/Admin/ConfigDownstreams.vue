<script setup lang="ts">
import type { Downstream, AdminSystem } from '~/composables/useAdmin'
const props = defineProps<{ modelValue: Downstream[]; systems: AdminSystem[] }>()
const emit = defineEmits<{ 'update:modelValue': [Downstream[]] }>()
const ds = computed({ get: () => props.modelValue ?? [], set: v => emit('update:modelValue', v) })
const add = () => ds.value = [...ds.value, { url: '', apiKey: '', disabled: false, systems: {} }]
</script>
<template>
  <div class="space-y-3">
    <div class="flex justify-end"><UButton icon="i-heroicons-plus" size="sm" variant="soft" @click="add">Add Downstream</UButton></div>
    <div v-if="!ds.length" class="text-center text-neutral-500 py-8 text-sm">No downstream instances configured.</div>
    <div v-for="(d, i) in ds" :key="i" class="border border-neutral-800 rounded-lg p-4 space-y-3">
      <div class="grid grid-cols-2 gap-3">
        <UFormField label="URL" class="col-span-2"><UInput v-model="d.url" placeholder="https://other.rdio-scanner.example.com" /></UFormField>
        <UFormField label="API Key"><UInput v-model="d.apiKey" /></UFormField>
      </div>
      <div class="flex items-center justify-between">
        <UCheckbox v-model="d.disabled" label="Disabled" />
        <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="sm" @click="ds.value = ds.value.filter((_,idx)=>idx!==i)">Remove</UButton>
      </div>
    </div>
  </div>
</template>
