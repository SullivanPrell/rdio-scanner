<script setup lang="ts">
const admin = useAdmin()
const router = useRouter()

const password = ref('')
const loading = ref(false)
const error = ref('')

// Already logged in → redirect
if (admin.isLoggedIn.value) {
  await navigateTo('/admin/config')
}

const login = async () => {
  if (!password.value) return
  loading.value = true
  error.value = ''
  const ok = await admin.login(password.value)
  if (ok) {
    await router.push('/admin/config')
  } else {
    error.value = 'Incorrect password'
  }
  loading.value = false
}

useHead({ title: 'Admin Login – Rdio Scanner' })
</script>

<template>
  <div class="min-h-screen flex items-center justify-center p-4" style="background: var(--scanner-bg)">
    <UCard class="w-full max-w-sm">
      <template #header>
        <div class="flex items-center gap-3">
          <div class="led active" />
          <span class="font-bold text-lg">Rdio Scanner</span>
        </div>
        <p class="text-sm text-neutral-400 mt-1">Admin login</p>
      </template>

      <div class="space-y-4">
        <UFormField label="Password" :error="error">
          <UInput
            v-model="password"
            type="password"
            placeholder="Admin password"
            autofocus
            :color="error ? 'error' : 'neutral'"
            @keydown.enter="login"
          />
        </UFormField>
        <UButton block :loading="loading" @click="login">
          Sign in
        </UButton>
      </div>

      <template #footer>
        <NuxtLink to="/" class="text-xs text-neutral-500 hover:text-neutral-300">
          ← Back to scanner
        </NuxtLink>
      </template>
    </UCard>
  </div>
</template>
