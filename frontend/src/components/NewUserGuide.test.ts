import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { fixture, flush } from '../test-utils/vueFixture.ts'

function setup(t: TestContext) {
  const saved = new Map<string, string>()
  const f = fixture(t, {
    getItem: key => saved.get(key) ?? null,
    setItem: (key, value) => { saved.set(key, value) },
    removeItem: key => { saved.delete(key) },
  })
  const guides = f.evaluate(readFileSync(new URL('../config/contextualGuides.ts', import.meta.url), 'utf8'), {})
  const router = { push: async () => {} }
  const dependencies = {
    '@/config/contextualGuides': guides,
    'vue-router': { useRouter: () => router },
  }
  const mount = () => f.component(new URL('./NewUserGuide.vue', import.meta.url), dependencies)
  return { ...f, mount, router, guides, saved }
}

test('the tour never auto-opens; the manual event shows the sidebar plus chat composer steps', async t => {
  const f = setup(t)
  const { vm } = f.mount()
  t.mock.timers.tick(2000)
  await flush()
  assert.equal(vm.active, false, 'no auto-open on mount')

  f.guides.openNewUserGuide()
  assert.equal(vm.active, true)
  assert.deepEqual(Array.from(vm.steps, (s: any) => s.key), ['overview'])
  assert.deepEqual(Array.from(vm.steps[0].targets, (t: any) => t.selector), [
    '[data-guide="sidebar-toggle"]',
    '[data-guide="chat-kb-mention"]',
    '[data-guide="chat-attachment"]',
  ])
  assert.deepEqual(Array.from(vm.steps[0].targets, (t: any) => t.itemKey), ['sidebar', 'kb', 'attach'])
})

test('finishing the tour marks both done keys and the chat page tour', async t => {
  const f = setup(t)
  const { vm } = f.mount()
  f.guides.openNewUserGuide()
  vm.onFinish()
  assert.equal(f.saved.get(f.guides.GLOBAL_USER_GUIDE_KEY), '1')
  assert.equal(f.guides.isGlobalUserGuideDone(), true)
  assert.equal(f.saved.get(f.guides.CONTEXTUAL_GUIDE_TOURS.chat.storageKey), '1',
    'the chat contextual tour would repeat the same highlights right after')
})

test('repeated open events while active are ignored', async t => {
  const f = setup(t)
  const { vm } = f.mount()
  f.guides.openNewUserGuide()
  f.guides.openNewUserGuide()
  assert.equal(vm.active, true)
})
