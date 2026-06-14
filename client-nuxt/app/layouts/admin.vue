<script setup lang="ts">
const admin = useAdmin()
const router = useRouter()
const route = useRoute()

// Guard: redirect to login if not authenticated
onMounted(async () => {
  if (!admin.isLoggedIn.value) {
    await router.push('/admin')
  }
})

const logout = async () => {
  await admin.logout()
  await router.push('/admin')
}

const navItems = [
  { label: 'Config', icon: 'i-heroicons-cog-6-tooth', to: '/admin/config' },
  { label: 'Tools', icon: 'i-heroicons-wrench-screwdriver', to: '/admin/tools' },
  { label: 'Logs', icon: 'i-heroicons-document-text', to: '/admin/logs' },
]

const isActive = (path: string) => route.path.startsWith(path)
</script>

<template>
  <div class="min-h-screen flex flex-col" style="background: var(--scanner-bg)">
    <!-- Top nav -->
    <header class="border-b border-neutral-800 px-4 py-3">
      <div class="max-w-6xl mx-auto flex items-center gap-4">
        <NuxtLink to="/" class="flex items-center gap-2 text-sm text-neutral-400 hover:text-white">
          <span class="led active" />
          <span class="font-bold">Rdio Scanner</span>
        </NuxtLink>

        <nav class="flex items-center gap-1 ml-4">
          <NuxtLink
            v-for="item in navItems"
            :key="item.to"
            :to="item.to"
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm transition-colors"
            :class="isActive(item.to)
              ? 'bg-neutral-800 text-white'
              : 'text-neutral-400 hover:text-white hover:bg-neutral-900'"
          >
            <UIcon :name="item.icon" class="size-4" />
            {{ item.label }}
          </NuxtLink>
        </nav>

        <div class="ml-auto">
          <UButton
            icon="i-heroicons-arrow-right-on-rectangle"
            variant="ghost"
            color="neutral"
            size="sm"
            @click="logout"
          >
            Logout
          </UButton>
        </div>
      </div>
    </header>

    <!-- Page content -->
    <main class="flex-1 max-w-6xl mx-auto w-full px-4 py-6">
      <slot />
    </main>
  </div>
</template>
