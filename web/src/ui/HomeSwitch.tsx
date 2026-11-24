// 两种页面手动选：3D 地形和频谱。第一次按设备检测的结果来，之后用户选了就记住，检测不再替他改（D85）。
import { useT } from '../lib/i18n'
import { forceTier } from '../lib/device'
import { Seg } from './Seg'

export function HomeSwitch({ current }: { current: '3d' | 'spec' }) {
  const t = useT()
  return (
    <span className="home-switch" data-home-switch>
      <Seg label={t('home.switch')} value={current} onChange={(v) => {
        if (v === current) return
        forceTier(v === '3d' ? 'high' : 'low')
        location.assign(v === '3d' ? '?page=hero' : '?page=about')
      }} options={[{ value: '3d', label: t('home.3d') }, { value: 'spec', label: t('home.spec') }]} />
    </span>
  )
}
