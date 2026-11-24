import { loadLimits } from './lib/limits'
import { StrictMode, lazy, Suspense, useEffect } from 'react'

// iPhone、iPad：字号小于 16px 的输入框一点进去，Safari 会自动放大整页，固定高度的下单区会被撑乱（11-18 Safari 内核检查）。
// 加 maximum-scale=1 只关掉这个自动放大，双指缩放照样能用（iOS 10 起不认禁止缩放）；安卓上这样写会禁止缩放，所以只给苹果的设备
if (/iP(hone|ad|od)/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)) {
  const vp = document.querySelector('meta[name="viewport"]')
  if (vp && !/maximum-scale/.test(vp.getAttribute('content') ?? '')) vp.setAttribute('content', `${vp.getAttribute('content')}, maximum-scale=1`)
}
import { createRoot } from 'react-dom/client'
// 字体自托管（全部 SIL OFL 1.1，可商用；决策 D21、D24）。
// Noto Sans SC 就是思源黑体；按字符分片，浏览器只下载页面实际用到的那几片。
import '@fontsource-variable/geist'
import '@fontsource-variable/geist-mono'
import './styles/cjk.css'
import './styles/tokens.css'
import './styles/base.css'
import './styles/ui.css'
import { installHairline } from './lib/pixel'
import { installPrivacyHotkey } from './lib/privacy'
import { installTips } from './lib/tips'
import { refreshSession, useSession } from './lib/session'
import { openWizard, wizardPending } from './lib/follow'
import { usePage } from './lib/route'

installHairline()
installPrivacyHotkey()
installTips()
void refreshSession()

// 可安装应用（18 第 6.7 条）：只在正式构建里注册；后台脚本只管"连不上时显示说明页"，不缓存任何东西
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => { void navigator.serviceWorker.register('/sw.js').catch(() => undefined) })
}

const Showroom = lazy(() => import('./pages/Showroom'))
const Market = lazy(() => import('./pages/Market'))
const Auth = lazy(() => import('./pages/Auth'))
const Account = lazy(() => import('./pages/Account'))
const Admin = lazy(() => import('./pages/Admin'))
const Ai = lazy(() => import('./pages/Ai'))
const Palette = lazy(() => import('./ui/Palette').then((m) => ({ default: m.Palette })))
const GlossaryHost = lazy(() => import('./ui/Palette').then((m) => ({ default: m.GlossaryHost })))
const Risk = lazy(() => import('./pages/Risk'))
const Review = lazy(() => import('./pages/Review'))
const Landing = lazy(() => import('./pages/Landing'))
const About = lazy(() => import('./pages/About'))
const Hero = lazy(() => import('./pages/Hero'))
const Overview = lazy(() => import('./pages/Landing').then((m) => ({ default: m.Overview })))
const WizardHost = lazy(() => import('./pages/ai/Wizard').then((m) => ({ default: m.WizardHost })))
const HighlightToast = lazy(() => import('./ui/HighlightToast').then((m) => ({ default: m.HighlightToast })))

function Root() {
  return (
    <>
      <Page />
      <Suspense fallback={null}><Follower /><Palette /><GlossaryHost /><HighlightBar /></Suspense>
    </>
  )
}

/** 跟随向导全站一份。注册后第一次进来自动打开，可以跳过（3.18）。 */
function Follower() {
  const s = useSession()
  const page = usePage()
  const inn = s.state === 'in'
  // 这台服务器上还没有任何策略（刚装好时就是这样，我们不内置策略）就先不弹：弹出来也没东西可选。
  // 记号留着，等有了策略、下次进来再弹；帮助菜单里随时能打开
  useEffect(() => {
    if (!inn || page === 'login' || !wizardPending()) return
    let off = false
    fetch('/api/strategies', { cache: 'no-store' }).then((r) => (r.ok ? r.json() : null)).then((l: unknown) => {
      if (!off && Array.isArray(l) && l.length > 0) openWizard()
    }).catch(() => undefined)
    return () => { off = true }
  }, [inn, page])
  return inn ? <WizardHost /> : null
}

/** 新高光：顶部亮一条（D91） */
function HighlightBar() {
  const s = useSession()
  return s.state === 'in' ? <HighlightToast /> : null
}

/** 没登录先看到落地页；已登录直接进上次用的交易账户（D75 第 6、20 条） */
function Home() {
  const s = useSession()
  if (s.state === 'loading') return null
  if (s.state === 'in') return <Market />
  // 系统开了减少动态效果：不放地形和开场，直接方块图
  return matchMedia('(prefers-reduced-motion: reduce)').matches ? <Landing /> : <Hero />
}

function Page() {
  const page = usePage()
  switch (page) {
    case 'showroom': return import.meta.env.DEV ? <Showroom /> : <Market />
    case 'login': return <Auth />
    case 'about': return <About />
    case 'landing': return <Landing />
    case 'hero': return <Hero />
    case 'account': return <Account />
    case 'admin': return <Admin />
    case 'ai': return <Ai />
    case 'market': return <Overview />
    case 'risk': return <Risk />
    case 'review': return <Review />
    case 'trade': return <Market />
    default: return <Home />
  }
}

loadLimits() // 部署的人改的上限（D103）
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Suspense fallback={null}>
      <Root />
    </Suspense>
  </StrictMode>,
)
