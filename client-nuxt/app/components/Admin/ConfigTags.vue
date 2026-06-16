<script setup lang="ts">
import type { Tag } from '~/composables/useAdmin'
const props = defineProps<{ modelValue: Tag[] }>()
const emit = defineEmits<{ 'update:modelValue': [Tag[]] }>()
const tags = computed({ get: () => props.modelValue ?? [], set: v => emit('update:modelValue', v) })
const add = () => tags.value = [...tags.value, { label: '' }]
</script>
<template>
  <div class="space-y-3">
    <div class="flex justify-end"><UButton icon="i-heroicons-plus" size="sm" variant="soft" @click="add">Add Tag</UButton></div>
    <div class="space-y-2 max-w-md">
      <div v-for="(t, i) in tags" :key="i" class="flex gap-2 items-center">
        <UInput v-model="t.label" class="flex-1" placeholder="Tag name" />
        <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="sm" @click="tags.value = tags.value.filter((_,idx)=>idx!==i)" />
      </div>
      <div v-if="!tags.length" class="text-neutral-500 text-sm py-4 text-center">No tags configured.</div>
    </div>
  </div>
</template>
