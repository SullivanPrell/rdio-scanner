<script setup lang="ts">
import type { AdminConfig } from '~/composables/useAdmin'

definePageMeta({ layout: 'admin' })

const admin = useAdmin()
const toast = useToast()
const router = useRouter()
const route = useRoute()

const cfg = ref<AdminConfig | null>(null)
const loading = ref(true)
const saving = ref(false)

const tabs = [
  { key: 'systems', label: 'Systems', icon: 'i-heroicons-radio' },
  { key: 'dirwatch', label: 'Dir Watch', icon: 'i-heroicons-folder-open' },
  { key: 'apikeys', label: 'API Keys', icon: 'i-heroicons-key' },
  { key: 'access', label: 'Access', icon: 'i-heroicons-lock-closed' },
  { key: 'bridge', label: 'Bridge', icon: 'i-heroicons-signal' },
  { key: 'downstreams', label: 'Downstreams', icon: 'i-heroicons-arrow-right-circle' },
  { key: 'groups', label: 'Groups', icon: 'i-heroicons-squares-2x2' },
  { key: 'tags', label: 'Tags', icon: 'i-heroicons-tag' },
  { key: 'options', label: 'Options', icon: 'i-heroicons-adjustments-horizontal' },
]

// Keep the active tab in the URL (?tab=bridge) so config sub-pages are
// deep-linkable and survive a refresh. This MUST reference the `route` from
// `useRoute()` declared above — a bare `route` here throws "route is not
// defined" and blanks the whole config page, including the Bridge submenu.
const isTabKey = (v: unknown): v is string =>
  typeof v === 'string' && tabs.some(t => t.key === v)

const activeTab = ref(isTabKey(route.query.tab) ? route.query.tab : 'systems')

watch(() => route.query.tab, (tab) => {
  if (isTabKey(tab)) activeTab.value = tab
})
watch(activeTab, (tab) => {
  if (route.query.tab !== tab) router.replace({ query: { ...route.query, tab } })
})

onMounted(async () => {
  cfg.value = await admin.getConfig()
  loading.value = false
  if (!cfg.value && !admin.isLoggedIn.value) {
    await router.push('/admin')
  }
})

const save = async () => {
  if (!cfg.value) return
  saving.value = true
  await admin.saveConfig(cfg.value)
  saving.value = false
}

useHead({ title: 'Config – Admin – Rdio Scanner' })
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-bold">Configuration</h1>
      <UButton
        icon="i-heroicons-check"
        :loading="saving"
        :disabled="!cfg"
        @click="save"
      >
        Save
      </UButton>
    </div>

    <UAlert v-if="cfg?.docker" color="info" icon="i-heroicons-information-circle">
      Running in Docker — dir watch is disabled.
    </UAlert>

    <div v-if="loading" class="flex justify-center py-16">
      <UIcon name="i-heroicons-arrow-path" class="size-6 animate-spin text-neutral-400" />
    </div>

    <template v-else-if="cfg">
      <!-- Tab navigation -->
      <div class="flex flex-wrap gap-1 border-b border-neutral-800 pb-2">
        <button
          v-for="tab in tabs"
          :key="tab.key"
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm transition-colors"
          :class="activeTab === tab.key
            ? 'bg-neutral-800 text-white'
            : 'text-neutral-400 hover:text-white hover:bg-neutral-900'"
          @click="activeTab = tab.key"
        >
          <UIcon :name="tab.icon" class="size-3.5" />
          {{ tab.label }}
        </button>
      </div>

      <!-- Tab content -->
      <div class="mt-4">
        <AdminConfigSystems v-if="activeTab === 'systems'" v-model="cfg.systems" :groups="cfg.groups" :tags="cfg.tags" />
        <AdminConfigDirwatch v-else-if="activeTab === 'dirwatch'" v-model="cfg.dirwatch" :systems="cfg.systems" :docker="cfg.docker" />
        <AdminConfigApikeys v-else-if="activeTab === 'apikeys'" v-model="cfg.apikeys" :systems="cfg.systems" />
        <AdminConfigAccess v-else-if="activeTab === 'access'" v-model="cfg.access" :systems="cfg.systems" />
        <AdminConfigBridge v-else-if="activeTab === 'bridge'" v-model="cfg.bridge" :systems="cfg.systems" />
        <AdminConfigDownstreams v-else-if="activeTab === 'downstreams'" v-model="cfg.downstreams" :systems="cfg.systems" />
        <AdminConfigGroups v-else-if="activeTab === 'groups'" v-model="cfg.groups" />
        <AdminConfigTags v-else-if="activeTab === 'tags'" v-model="cfg.tags" />
        <AdminConfigOptions v-else-if="activeTab === 'options'" v-model="cfg.options" />
      </div>
    </template>

    <div v-else class="text-center text-neutral-500 py-16">
      Failed to load configuration.
      <UButton variant="link" @click="$router.push('/admin')">Re-login</UButton>
    </div>
  </div>
</template>
