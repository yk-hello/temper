import { useEnterExit } from '../../lib/rowmotion'

/** 列表行进出场（D68）：新行滑进来，消失的行淡出。第一次渲染不动。 */
export function useRowsMotion<T>(items: T[], key: (x: T) => string) {
  return useEnterExit(items, key)
}
