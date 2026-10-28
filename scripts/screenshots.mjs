// README screenshots and a browser smoke test, against a demo instance (DEMO_DATA=true seeds a household's requests):
//
//   DATA_DIR=/tmp/requests-demo DEMO_DATA=true ADMIN_EMAIL=demo@example.com ADMIN_PASSWORD=demodemo1 ADMIN_NAME=Maya PORT=18310 \
//     RADARR_URL=… RADARR_API_KEY=… SONARR_URL=… SONARR_API_KEY=… ./pocket-requests     # services make Discover and search real
//   CDP=http://127.0.0.1:19310 BASE=http://127.0.0.1:18310 node scripts/screenshots.mjs   # any Chrome with remote debugging
//   SHOTS=0 … node scripts/screenshots.mjs                                                  # checks only
//   NOLOGIN=1 … for an AUTH_MODE=none instance
// playwright-core comes from web/node_modules, or set PW_FROM to a package.json next to an install of it.
import { createRequire } from 'node:module'
import assert from 'node:assert/strict'

const require = createRequire(process.env.PW_FROM || new URL('../web/package.json', import.meta.url))
let pw
try { pw = require('playwright') } catch { pw = require('playwright-core') }
const { chromium } = pw
const base = process.env.BASE || 'http://127.0.0.1:18310'
const shots = process.env.SHOTS !== '0'
const dir = process.env.OUT || new URL('../docs/screenshots/', import.meta.url).pathname
const browser = process.env.CDP ? await chromium.connectOverCDP(process.env.CDP) : await chromium.launch()
const errors = []

async function session({ phone = false, dark = false } = {}) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: phone ? 3 : 2, isMobile: phone, hasTouch: phone, colorScheme: dark ? 'dark' : 'light',
    userAgent: phone ? 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1' : undefined,
  })
  const page = await ctx.newPage()
  page.on('pageerror', e => errors.push(`pageerror: ${e}`))
  page.on('console', m => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errors.push(m.text()) })
  if (!process.env.NOLOGIN) {
    await page.goto(base + '/login')
    const res = await page.evaluate(() => fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email: 'demo@example.com', password: 'demodemo1' }) }).then(r => r.status))
    assert.equal(res, 200, 'demo sign-in')
  }
  return { ctx, page }
}
async function go(page, path, wait = 1500) {
  await page.goto(base + path, { waitUntil: 'networkidle' })
  // headless Chrome freezes CSS fade-ins: show everything at once
  await page.addStyleTag({ content: '*, *::before, *::after { transition: none !important; animation: none !important; }' })
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(wait)
}
async function shot(page, name) { if (shots) await page.screenshot({ path: dir + name + '.png' }) }
async function text(page) { return (await page.content()).toLowerCase() }

// ---- desktop ----
{
  const { ctx, page } = await session()
  await go(page, '/')
  assert.ok((await text(page)).includes('dune: part two'), 'Home shows what is on the way')
  await shot(page, 'desktop-home')
  await go(page, '/discover', 3500)
  await shot(page, 'desktop-discover')
  await go(page, '/requests')
  assert.ok((await text(page)).includes('the substance'), 'My requests lists the requests')
  await shot(page, 'desktop-requests')
  const id = await page.evaluate(() => fetch('/api/v1/requests?q=The%20Substance').then(r => r.json()).then(d => d.items[0].id))
  await go(page, '/requests/' + id)
  await shot(page, 'desktop-request')
  await go(page, '/title/movie/693134', 2500)
  await shot(page, 'desktop-title')
  await go(page, '/approvals')
  assert.ok((await text(page)).includes('inside out 2'), 'Approvals lists the waiting requests')
  await shot(page, 'desktop-approvals')
  await go(page, '/care')
  await shot(page, 'desktop-care')
  for (const p of ['/search?q=severance', '/upcoming', '/downloads', '/problems', '/title/series/371980', '/settings/services', '/settings/requests', '/settings/notifications', '/settings/data', '/settings/people']) await go(page, p, 900)
  await ctx.close()
}

// ---- phone ----
{
  const { ctx, page } = await session({ phone: true, dark: true })
  await go(page, '/')
  await shot(page, 'phone-home')
  await go(page, '/title/movie/693134', 2500)
  await shot(page, 'phone-title')
  await go(page, '/requests')
  await shot(page, 'phone-requests')
  await ctx.close()
}

await browser.close()
if (errors.length) {
  console.error('browser errors:\n' + errors.join('\n'))
  process.exit(1)
}
console.log('ok')
