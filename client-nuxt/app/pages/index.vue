<script setup lang="ts">
const rs = useRdioScanner()

const showSelect = ref(false)
const showSearch = ref(false)
const showPin    = ref(false)
const pinInput   = ref('')
const pinError   = ref(false)

onMounted(() => rs.connect())
onUnmounted(() => rs.disconnect())

watch(rs.pinRequired, (v) => { if (v) showPin.value = true })

const submitPin = () => {
  if (!pinInput.value) return
  rs.submitPin(pinInput.value)
  pinError.value = false
  setTimeout(() => {
    if (rs.pinRequired.value) pinError.value = true
    else showPin.value = false
  }, 800)
}

useHead({ title: 'Rdio Scanner' })
</script>

<template>
  <div class="scanner-root">

    <!-- PIN modal -->
    <UModal v-model:open="showPin" :dismissible="false" title="PIN Required">
      <template #body>
        <div class="space-y-3">
          <p class="text-sm text-neutral-400">This scanner is PIN protected.</p>
          <UInput v-model="pinInput" type="password" placeholder="Unlock code" autofocus
            :color="pinError ? 'error' : 'neutral'" @keydown.enter="submitPin" />
          <p v-if="pinError" class="text-xs text-red-400">Incorrect code, try again.</p>
        </div>
      </template>
      <template #footer>
        <UButton block @click="submitPin">Unlock</UButton>
      </template>
    </UModal>

    <!-- Max listeners modal -->
    <UModal v-model:open="rs.maxReached.value" :dismissible="false" title="Maximum Listeners">
      <template #body>
        <p class="text-sm text-neutral-400">
          The maximum number of simultaneous listeners has been reached. Please try again later.
        </p>
      </template>
    </UModal>

    <div class="scanner-wrap">

      <!-- Status + display -->
      <ScannerDisplay
        :call="rs.currentCall.value"
        :config="rs.config.value"
        :history="rs.callHistory.value"
        :is-playing="rs.isPlaying.value"
        :is-paused="rs.isPaused.value"
        :progress="rs.playbackProgress.value"
        :listeners-count="rs.listenersCount.value"
        :show-listeners-count="rs.config.value?.showListenersCount ?? false"
        :time12h="rs.config.value?.time12hFormat ?? false"
        :connected="rs.connected.value"
        :clock="rs.clock.value"
        :freq="rs.currentFreq.value"
        :error="rs.currentError.value"
        :spike="rs.currentSpike.value"
        :unit="rs.currentUnit.value"
        :led-color="rs.talkgroupLed.value"
        :queue-size="rs.callQueue.value.length"
      />

      <!-- Controls -->
      <ScannerControls
        :livefeed-online="rs.livefeedOnline.value"
        :livefeed-offline="rs.livefeedOffline.value"
        :livefeed-paused="rs.livefeedPaused.value"
        :playback-mode="rs.playbackMode.value"
        :hold-sys="rs.holdSys.value"
        :hold-tg="rs.holdTg.value"
        :is-playing="rs.isPlaying.value"
        :is-paused="rs.isPaused.value"
        @livefeed="rs.toggleLivefeed()"
        @hold-system="rs.toggleHoldSystem()"
        @hold-talkgroup="rs.toggleHoldTalkgroup()"
        @replay="rs.replay()"
        @skip="rs.skip()"
        @avoid="rs.avoid()"
        @search="showSearch = !showSearch; showSelect = false"
        @pause="rs.toggleLivefeedPause()"
        @select="showSelect = !showSelect; showSearch = false"
      />

      <!-- Admin link -->
      <div class="admin-link">
        <NuxtLink to="/admin">Admin</NuxtLink>
      </div>
    </div>

    <!-- Slide-in panel -->
    <Transition name="slide">
      <div v-if="showSelect || showSearch" class="side-panel">
        <ScannerSelectPanel
          v-if="showSelect"
          :config="rs.config.value"
          :livefeed-map="rs.livefeedMap.value"
          @toggle-system="rs.toggleSystem"
          @toggle-talkgroup="rs.toggleTalkgroup"
          @close="showSelect = false"
        />
        <ScannerSearchPanel
          v-else-if="showSearch"
          :config="rs.config.value"
          :list-result="rs.listResult.value"
          @search="rs.searchCalls"
          @play-call="rs.requestCall"
          @close="showSearch = false"
        />
      </div>
    </Transition>

  </div>
</template>

<style scoped>
.scanner-root {
  min-height: 100vh;
  background: #1a1a1a;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  position: relative;
  overflow: hidden;
}
.scanner-wrap {
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  max-width: 640px;
  min-width: 0;
  padding: 24px;
  user-select: none;
  width: 100%;
}
.admin-link {
  text-align: center;
  margin-top: 8px;
}
.admin-link a {
  color: rgba(255,255,255,.2);
  font-size: 12px;
  text-decoration: none;
}
.admin-link a:hover {
  color: rgba(255,255,255,.5);
}

/* Side panel */
.side-panel {
  position: fixed;
  inset: 0;
  background: #111;
  z-index: 50;
  display: flex;
  flex-direction: column;
  overflow-y: auto;
}
.slide-enter-active, .slide-leave-active { transition: transform .2s ease; }
.slide-enter-from, .slide-leave-to { transform: translateX(100%); }
</style>
