// 邮箱错误提示：说清楚哪里不对；能猜到完整写法时给一个"改成这个"按钮
import { errText, useT } from '../lib/i18n'
import type { EmailIssue } from '../lib/email'

export function EmailError({ issue, onFix }: { issue: EmailIssue; onFix: (v: string) => void }) {
  const t = useT()
  const fix = issue.fix
  return (
    <>
      {errText(issue.code, fix ? { fix } : undefined)}
      {fix && <button type="button" className="fld-fix" onClick={() => onFix(fix)}>{t('auth.useFix')}</button>}
    </>
  )
}
