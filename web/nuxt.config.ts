export default defineNuxtConfig({
  // 纯 SPA: 一切数据经 relay, 无 SSR
  ssr: false,
  devtools: { enabled: false },
  app: {
    head: {
      title: "AnyView Next",
      meta: [
        { name: "viewport", content: "width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content" },
        { name: "theme-color", content: "#101114" },
      ],
      htmlAttrs: { lang: "zh-CN" },
    },
  },
  css: [
    "@fontsource/fira-code/latin-400.css",
    "@fontsource/fira-code/latin-500.css",
    "@fontsource/fira-code/latin-600.css",
    "~/assets/css/main.css",
  ],
  vite: {
    build: {
      rolldownOptions: {
        output: {
          codeSplitting: {
            groups: [
              { name: "vendor-lezer", test: /node_modules\/@lezer/ },
              { name: "vendor-codemirror", test: /node_modules\/(@codemirror|codemirror)/ },
              { name: "vendor-shiki-langs", test: /node_modules\/@shikijs\/langs/ },
              { name: "vendor-shiki", test: /node_modules\/(shiki|@shikijs|oniguruma-to-es)/ },
              { name: "vendor-dompurify", test: /node_modules\/dompurify/ },
              { name: "vendor-icons", test: /node_modules\/lucide-vue-next/ },
            ],
          },
        },
      },
    },
  },
  // 开发期把 /api 代理到 relay(生产由 relay -web 直接同源托管)
  // 注意：必须放 nitro.devProxy——Nuxt4 顶层 devProxy 会被静默丢弃(实测 /api 落 SPA fallback)
  nitro: {
    devProxy: {
      "/api": { target: "http://127.0.0.1:8787/api", changeOrigin: true },
    },
  },
  compatibilityDate: "2026-09-01",
});
