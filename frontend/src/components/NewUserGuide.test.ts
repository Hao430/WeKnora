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

test('new users get the simplified tour: sidebar plus the chat composer steps', async t => {
  const f = setup(t)
  const { vm } = f.mount()
  t.mock.timers.tick(700)
  await flush()
  assert.equal(vm.active, true)
  assert.deepEqual(Array.from(vm.steps, (s: any) => s.key), ['sidebar', 'kbMention', 'attachment'])
  assert.equal(vm.steps[0].target, '[data-guide="sidebar-toggle"]')
  assert.equal(vm.steps[1].target, '[data-guide="chat-kb-mention"]')
  assert.equal(vm.steps[2].target, '[data-guide="chat-attachment"]')
})

test('finishing the tour marks both done keys and the chat page tour', async t => {
  const f = setup(t)
  const { vm } = f.mount()
  t.mock.timers.tick(700)
  await flush()
  vm.onFinish()
  assert.equal(f.saved.get(f.guides.GLOBAL_USER_GUIDE_KEY), '1')
  assert.equal(f.guides.isGlobalUserGuideDone(), true)
  assert.equal(f.saved.get(f.guides.CONTEXTUAL_GUIDE_TOURS.chat.storageKey), '1',
    'the chat contextual tour would repeat the same highlights right after')
})

test('done users are not auto-shown, but the manual event still opens', async t => {
  const f = setup(t)
  f.saved.set(f.guides.GLOBAL_USER_GUIDE_KEY, '1')
  const first = f.mount()
  t.mock.timers.tick(2000)
  await flush()
  assert.equal(first.vm.active, false)
  f.guides.openNewUserGuide()
  assert.equal(first.vm.active, true)
  first.close()

  const pending = f.mount()
  pending.close()
  t.mock.timers.tick(1000)
  assert.equal(pending.vm.active, false)
})
