// https://nuxt.com/docs/api/configuration/nuxt-config
export default defineNuxtConfig({
  ssr: false,

  compatibilityDate: '2025-05-15',

  modules: ['@nuxt/ui'],

  css: ['~/assets/css/main.css'],

  ui: {
    theme: {
      colors: ['primary', 'secondary', 'success', 'info', 'warning', 'error'],
    },
  },

  // Proxy /api/* and WebSocket to the Go server during development
  nitro: {
    devProxy: {
      '/api': {
        target: 'http://localhost:3000/api',
        ws: true,
        changeOrigin: true,
      },
      '/ws': {
        target: 'ws://localhost:3000/',
        ws: true,
        changeOrigin: true,
      },
    },
  },

  app: {
    head: {
      title: 'Rdio Scanner',
      meta: [
        { charset: 'utf-8' },
        { name: 'viewport', content: 'width=device-width, initial-scale=1' },
        { name: 'theme-color', content: '#0f0f0f' },
      ],
      link: [
        { rel: 'icon', type: 'image/x-icon', href: '/favicon.ico' },
      ],
    },
  },

  typescript: {
    strict: true,
  },

  devtools: { enabled: false },
})
