<script setup lang="ts">
import type { Group } from '~/composables/useAdmin'
const props = defineProps<{ modelValue: Group[] }>()
const emit = defineEmits<{ 'update:modelValue': [Group[]] }>()
const groups = computed({ get: () => props.modelValue, set: v => emit('update:modelValue', v) })
const add = () => groups.value = [...groups.value, { label: '' }]
</script>
<template>
  <div class="space-y-3">
    <div class="flex justify-end"><UButton icon="i-heroicons-plus" size="sm" variant="soft" @click="add">Add Group</UButton></div>
    <div class="space-y-2 max-w-md">
      <div v-for="(g, i) in groups" :key="i" class="flex gap-2 items-center">
        <UInput v-model="g.label" class="flex-1" placeholder="Group name" />
        <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="sm" @click="groups.value = groups.value.filter((_,idx)=>idx!==i)" />
      </div>
      <div v-if="!groups.length" class="text-neutral-500 text-sm py-4 text-center">No groups configured.</div>
    </div>
  </div>
</template>
