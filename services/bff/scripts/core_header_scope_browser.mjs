/** Lightweight readonly header/context check on the existing owned Core fixture. */
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const root = new URL('../../../', import.meta.url).pathname, base = 'http://127.0.0.1:3001';
const demo = JSON.parse(await fs.readFile(root + 'dev/local/bff-core-ui-demo.json', 'utf8'));
const { password } = JSON.parse(await fs.readFile(root + 'dev/local/accounts.json', 'utf8'));
const checks = [], errors = [], external = [], legacy = [], writes = [];
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
await context.route('**/*', route => {
  const request = route.request(), url = new URL(request.url());
  if (url.origin !== base) { external.push(url.origin + url.pathname); return route.abort(); }
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/bff/unconnected')) { legacy.push(url.pathname); return route.abort(); }
  if (!['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(url.pathname)) { writes.push({ path: url.pathname, method: request.method() }); return route.abort(); }
  return route.continue();
});
const page = await context.newPage(); page.setDefaultTimeout(20000); page.on('pageerror', error => errors.push(error.message));
const ok = check => { checks.push(check); console.log(JSON.stringify({ check })); };
const identity = async () => {
  const response = await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } }); assert(response.ok()); return response.json();
};
try {
  await page.goto(base + '/login'); await page.getByLabel('Логин', { exact: true }).fill('dev_admin'); await page.getByLabel('Пароль', { exact: true }).fill(password);
  const logged = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/login'); await page.getByRole('button', { name: 'Войти', exact: true }).click(); assert((await logged).ok()); await page.waitForURL('**/cabinet/**'); await page.waitForLoadState('networkidle');
  const initial = await identity(), organization = initial.organizations.find(item => item.id === demo.organization_id); assert(organization);
  const selector = page.getByLabel('Переключить кабинет и подразделение');
  for (const division of [demo.division_a, demo.division_b]) {
    const divisionDto = organization.divisions.find(item => item.id === division); assert(divisionDto);
    const value = `${demo.organization_id}:${division}`; const optionText = await selector.locator('option').filter({ hasText: divisionDto.name }).allTextContents(); assert(optionText.some(text => text.includes('Кабинет')));
    assert((await selector.locator('..').innerText()).includes(organization.name));
    const groups = await selector.locator('optgroup').evaluateAll(elements => elements.map(element => ({ label: element.label, values: [...element.querySelectorAll('option')].map(option => option.value) })));
    assert(groups.some(group => group.label === organization.name && group.values.includes(value)));
    if (await selector.inputValue() !== value) {
      const loaded = page.waitForEvent('load'); void loaded.catch(() => {});
      const switched = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/context'); await selector.selectOption(value); assert((await switched).ok()); await loaded; await page.waitForLoadState('networkidle');
    }
    const current = await identity(); assert.equal(current.user.organization_id, demo.organization_id); assert.equal(current.user.division_id, division); assert.equal(current.organizations.find(item => item.id === demo.organization_id)?.divisions.find(item => item.id === division)?.name, divisionDto.name); assert.equal(await selector.inputValue(), value);
    const reply = await context.request.get(base + '/bff/core/dashboard', { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': current.context_revision } }); assert(reply.ok()); const dashboard = (await reply.json()).data; assert.equal(dashboard.organization_id, demo.organization_id); assert.equal(dashboard.division_id, division); assert.equal(dashboard.division_name, divisionDto.name);
  }
  ok('header shows current organization/division names and switching reloads matching live scoped dashboard');
  await page.setViewportSize({ width: 390, height: 844 }); await selector.waitFor(); await page.waitForFunction(() => getComputedStyle(document.querySelector('.cabinet-main')).marginLeft === '0px'); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await page.screenshot({ path: root + 'dev/local/bff-core-header-scope-mobile.png' }); ok('header switch stays available without viewport overflow at390px');
  assert.deepEqual(errors, []); assert.deepEqual(external, []); assert.deepEqual(legacy, []); assert.deepEqual(writes, []); ok('readonly header check has zero business writes, JS errors, external or legacy requests');
} finally {
  await fs.writeFile(root + 'dev/local/bff-core-header-scope-browser-report.json', JSON.stringify({ run: demo.run, readOnly: true, checks, errors, external, legacy, writes }, null, 2)); await context.close(); await browser.close();
}
