// 可安装应用（PWA，18 第 6.7 条）的后台脚本，只做一件事：打开页面时连不上服务器，显示一张"连不上"的说明页。
// 不缓存页面、脚本和任何数据：行情和账户必须是实时的，旧的比没有更危险；也不会让更新后的页面卡在旧版本。
const CACHE = 'qs-offline-v1'
const OFFLINE = '/offline.html'

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.add(new Request(OFFLINE, { cache: 'reload' }))).then(() => self.skipWaiting()))
})

self.addEventListener('activate', (e) => {
  e.waitUntil(caches.keys().then((ks) => Promise.all(ks.filter((k) => k !== CACHE).map((k) => caches.delete(k)))).then(() => self.clients.claim()))
})

self.addEventListener('fetch', (e) => {
  if (e.request.mode !== 'navigate') return // 只管打开页面；接口、推送、脚本都直接走网络
  e.respondWith(fetch(e.request).catch(() => caches.match(OFFLINE)))
})
