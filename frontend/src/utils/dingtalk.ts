// DingTalk container helpers. Only the免登 (inner) flow needs the JSAPI; the
// browser path is a plain redirect and must keep working when the SDK cannot
// be loaded.

const DINGTALK_JSAPI_URL = 'https://g.alicdn.com/dingding/dingtalk-jsapi/3.0.25/dingtalk.open.js'
const JSAPI_TIMEOUT_MS = 5000

interface DingTalkJsapi {
  runtime?: {
    permission?: {
      requestAuthCode: (options: {
        corpId: string
        onSuccess: (result: { code?: string }) => void
        onFail: (error: unknown) => void
      }) => void
    }
  }
}

declare global {
  interface Window {
    dd?: DingTalkJsapi
  }
}

// DingTalk's embedded browser puts "AliApp(DingTalk/<version>)" in the UA.
// Scanning for "DingTalk" alone would also match a desktop browser that has
// the DingTalk extension installed.
export const isDingTalkContainer = (): boolean =>
  typeof navigator !== 'undefined' && /AliApp\(DingTalk\//i.test(navigator.userAgent)

const loadJsapi = (): Promise<DingTalkJsapi> =>
  new Promise((resolve, reject) => {
    if (window.dd?.runtime?.permission) {
      resolve(window.dd)
      return
    }
    const script = document.createElement('script')
    const timer = window.setTimeout(() => {
      script.remove()
      reject(new Error('DingTalk JSAPI load timed out'))
    }, JSAPI_TIMEOUT_MS)
    script.src = DINGTALK_JSAPI_URL
    script.onload = () => {
      window.clearTimeout(timer)
      if (window.dd?.runtime?.permission) resolve(window.dd)
      else reject(new Error('DingTalk JSAPI unavailable'))
    }
    script.onerror = () => {
      window.clearTimeout(timer)
      script.remove()
      reject(new Error('DingTalk JSAPI failed to load'))
    }
    document.head.appendChild(script)
  })

// requestAuthCode needs the corpId. Callers that cannot resolve it fall back to
// the scan flow instead of guessing.
export const requestDingTalkAuthCode = async (corpId: string): Promise<string> => {
  const dd = await loadJsapi()
  return new Promise((resolve, reject) => {
    dd.runtime?.permission?.requestAuthCode({
      corpId,
      onSuccess: (result) => {
        if (result?.code) resolve(result.code)
        else reject(new Error('DingTalk returned no auth code'))
      },
      onFail: (error) => reject(new Error(typeof error === 'string' ? error : 'DingTalk auth code request failed'))
    })
  })
}
