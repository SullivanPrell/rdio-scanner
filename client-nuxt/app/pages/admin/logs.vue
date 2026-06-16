<script setup lang="ts">
definePageMeta({ layout: 'admin' })

const admin = useAdmin()
const logs = ref<Awaited<ReturnType<typeof admin.getLogs>>>([])
const loading = ref(true)
const filter = ref('')

onMounted(async () => {
  logs.value = await admin.getLogs()
  loading.value = false
})

const levelColor = (level: string) => {
  switch (level?.toLowerCase()) {
    case 'error': return 'text-red-400'
    case 'warn': case 'warning': return 'text-yellow-400'
    case 'info': return 'text-blue-400'
    default: return 'text-neutral-400'
  }
}

const filtered = computed(() =>
  filter.value
    ? logs.value.filter(l =>
        l.message.toLowerCase().includes(filter.value.toLowerCase()) ||
        l.level.toLowerCase().includes(filter.value.toLowerCase()))
    : logs.value
)

useHead({ title: 'Logs – Admin – Rdio Scanner' })
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-bold">Logs</h1>
      <div class="flex gap-2">
        <UInput v-model="filter" placeholder="Filter…" icon="i-heroicons-magnifying-glass" size="sm" />
        <UButton
          icon="i-heroicons-arrow-path"
          variant="ghost"
          size="sm"
          :loading="loading"
          @click="async () => { loading = true; logs = await admin.getLogs(); loading = false }"
        />
      </div>
    </div>

    <div v-if="loading" class="flex justify-center py-16">
      <UIcon name="i-heroicons-arrow-path" class="size-6 animate-spin text-neutral-400" />
    </div>

    <div v-else class="font-mono text-xs rounded-lg border border-neutral-800 overflow-auto max-h-[70vh]">
      <div v-if="!filtered.length" class="p-4 text-neutral-500">No log entries.</div>
      <div
        v-for="(entry, i) in filtered"
        :key="i"
        class="flex gap-3 px-3 py-1.5 border-b border-neutral-800/50 hover:bg-neutral-900/50"
      >
        <span class="text-neutral-600 shrink-0 select-none">
          {{ new Date(entry.dateTime).toLocaleTimeString() }}
        </span>
        <span class="shrink-0 w-12" :class="levelColor(entry.level)">{{ entry.level }}</span>
        <span class="text-neutral-300 break-all">{{ entry.message }}</span>
      </div>
    </div>
  </div>
</template>
