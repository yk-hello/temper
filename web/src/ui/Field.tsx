// 表单输入框：标签在上，提示或错误在下（错误说清楚怎么改）。
// 错误出现时高度平滑展开，不让下面的内容跳。
import { useId, useState, type InputHTMLAttributes, type ReactNode } from 'react'
import { useT } from '../lib/i18n'

interface Props extends Omit<InputHTMLAttributes<HTMLInputElement>, 'onChange'> {
  label: string
  hint?: string
  error?: ReactNode
  value: string
  onChange: (v: string) => void
  reveal?: boolean // 密码框：显示一个"显示/隐藏"按钮
}

export function Field({ label, hint, error, value, onChange, reveal, type = 'text', className, ...rest }: Props) {
  const id = useId()
  const t = useT()
  const [shown, setShown] = useState(false)
  const msg = error || hint
  return (
    <div className={`fld ${error ? 'bad' : ''} ${className ?? ''}`}>
      <label className="fld-l" htmlFor={id}>{label}</label>
      <span className="fld-box">
        <input id={id} type={reveal && shown ? 'text' : type} value={value} onChange={(e) => onChange(e.target.value)}
          aria-invalid={!!error} aria-describedby={msg ? id + '-m' : undefined} spellCheck={false} {...rest} />
        {reveal && (
          <button type="button" className="fld-reveal" onClick={() => setShown(!shown)} aria-pressed={shown} aria-controls={id} tabIndex={-1}>
            {shown ? t('common.hide') : t('common.show')}
          </button>
        )}
      </span>
      <span className="fld-m" id={id + '-m'} aria-live="polite"><span>{msg}</span></span>
    </div>
  )
}

/** 整个表单的错误（服务器返回），左边一条红线 */
export function FormError({ text }: { text?: ReactNode }) {
  return (
    <div className={`form-err ${text ? 'on' : ''}`} role="alert" aria-live="assertive">
      <div><span>{text}</span></div>
    </div>
  )
}
