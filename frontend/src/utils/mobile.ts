export const MOBILE_BREAKPOINT = 720

/** 视口是否为移动端宽度（≤720px）。SSR/测试环境无 window 时恒为 false。 */
export function isMobileViewport(): boolean {
  return typeof window !== 'undefined'
    && typeof window.matchMedia === 'function'
    && window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT}px)`).matches
}
