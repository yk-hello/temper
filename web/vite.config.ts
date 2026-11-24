import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 开发时把 /api、/v1（qsig/1 策略接口）和 /ws 转发给本机后端（docker 里由 nginx 负责同样的事）
const backend = process.env.QS_BACKEND ?? 'http://localhost:8000'
const proxy = {
  '/api': backend,
  '/v1': backend,
  '/bapi': { target: backend, ws: true }, // 和币安一样的接口（模拟盘）和它的账户推送
  '/ws': { target: backend.replace(/^http/, 'ws'), ws: true },
}

export default defineConfig({
  plugins: [react()],
  server: { port: 5173, proxy },
  preview: { port: 4173, proxy },
})
