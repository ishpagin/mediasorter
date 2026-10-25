import path from 'node:path'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

// dev: `npm run dev` proxies the API to a running mediasorter-web (MS_API, default the server copy)
const api = process.env.MS_API ?? 'http://192.168.1.10:8511'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(__dirname, './src') } },
  server: { proxy: { '/api': api, '/preview': api, '/original': api, '/thumb': api } },
})
