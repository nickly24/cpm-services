/** Uploads dependency-unavailable proof only. Never imports or mutates business data. */
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';

const root = new URL('../../../', import.meta.url).pathname, base = 'http://127.0.0.1:3001';
const demo = JSON.parse(await fs.readFile(root + 'dev/local/bff-uploads-demo.json', 'utf8'));
const { password } = JSON.parse(await fs.readFile(root + 'dev/local/accounts.json', 'utf8'));
const checks = [], errors = [], external = [], legacy = [], businessWrites = [], responses = [];
let context, page, verified = false;
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const pass = name => { checks.push(name); console.log(JSON.stringify({ check: name })); };

async function actor(login, label) {
  if (context) await context.close();
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { external.push(url.origin + url.pathname); return route.abort(); }
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/bff/unconnected')) { legacy.push(url.pathname); return route.abort(); }
    if (!['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(url.pathname)) { businessWrites.push({ path: url.pathname, method: request.method() }); return route.abort(); }
    return route.continue();
  });
  page = await context.newPage(); page.setDefaultTimeout(25000);
  page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => dialog.accept());
  await page.goto(base + '/login'); await page.getByLabel('Логин', { exact: true }).fill(login); await page.getByLabel('Пароль', { exact: true }).fill(password);
  const auth = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/login' && response.request().method() === 'POST');
  await page.getByRole('button', { name: 'Войти', exact: true }).click(); assert.equal((await auth).status(), 200);
  await page.waitForURL('**/cabinet/**'); await page.waitForLoadState('networkidle');
  const selector = page.getByLabel('Переключить кабинет и подразделение'); await selector.waitFor();
  const value = `${demo.organization_id}:${demo.division_a}`;
  if (await selector.inputValue() !== value) {
    const loaded = page.waitForEvent('load'); void loaded.catch(() => {});
    const changed = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/context' && response.request().method() === 'POST');
    await selector.selectOption(value); assert.equal((await changed).status(), 200); await loaded; await page.waitForLoadState('networkidle');
  }
  const cabinet = login === 'dev_admin' ? 'admin' : 'staff_admin';
  const pending = ['catalog', 'sessions', 'jobs'].map(name => page.waitForResponse(response => new URL(response.url()).pathname === `/bff/uploads/${name}` && response.request().method() === 'GET'));
  pending.forEach(promise => { void promise.catch(() => {}); });
  await page.goto(`${base}/cabinet/${cabinet}/upload`); await page.getByRole('heading', { name: 'Загрузка', exact: true }).waitFor();
  const failures = await Promise.all(pending);
  for (const response of failures) {
    const data = await response.json();
    assert([502, 503, 504].includes(response.status()), 'Stopped owner must not return successful empty data');
    assert(['upstream_unavailable', 'dependency_unavailable', 'owner_unavailable'].includes(data.error), `Unexpected unavailable code: ${data.error}`);
    assert.notEqual(data.status, true); responses.push({ actor: label, path: new URL(response.url()).pathname, status: response.status(), code: data.error });
  }
  const alert = page.locator('main').getByRole('alert'); await alert.waitFor(); assert.match(await alert.innerText(), /недоступ|Не удалось|не удалось/);
  await page.getByText('Загружаем доступные форматы и журнал', { exact: true }).waitFor({ state: 'hidden' });
  assert.equal(await page.getByText('Загрузок пока нет.', { exact: true }).count(), 0);
  assert.equal(await page.getByText('Нет доступных форматов. Нужны права на загрузку и соответствующий раздел.', { exact: true }).count(), 0);
  assert.equal(await page.getByText('Импорт подтверждён', { exact: true }).count(), 0);
  assert.equal(await page.locator('main input[type="file"]').count(), 0);
  assert.equal(await page.getByRole('button', { name: 'Подтвердить импорт', exact: true }).count(), 0);
  pass(`${label} receives real owner-unavailable reads and a visible error without empty or successful import state`);
  assert.equal(await selector.inputValue(), value); assert.equal(await selector.isDisabled(), false);
  const retry = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/uploads/catalog' && response.request().method() === 'GET');
  await page.getByRole('button', { name: 'Обновить', exact: true }).click(); assert([502, 503, 504].includes((await retry).status())); await alert.waitFor();
  pass(`${label} keeps the cabinet switch and allows an explicit read refresh while owner is stopped`);
  await page.setViewportSize({ width: 390, height: 844 }); await page.waitForFunction(() => getComputedStyle(document.querySelector('.cabinet-main')).marginLeft === '0px');
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
  await page.screenshot({ path: root + `dev/local/bff-uploads-unavailable-${label}-mobile.png` });
  pass(`${label} unavailable state and header fit 390px without page overflow`);
}

try {
  await actor('dev_admin', 'admin'); await actor(demo.editor.login, 'editor');
  assert.deepEqual(errors, []); assert.deepEqual(external, []); assert.deepEqual(legacy, []); assert.deepEqual(businessWrites, []);
  pass('dependency-unavailable smoke uses only local BFF GET and auth with zero JS errors, business writes or fallback');
  verified = true;
} catch (error) {
  if (page) await page.screenshot({ path: root + 'dev/local/bff-uploads-unavailable-failure.png', fullPage: true, mask: [page.locator('input[type="password"]')] });
  throw error;
} finally {
  await fs.writeFile(root + 'dev/local/bff-uploads-unavailable-browser-report.json', JSON.stringify({ status: 'dependency_unavailable', unavailable_ui_verified: verified, live_uploads_pass: false, run: demo.run, owner: 'uploads', owner_port: 8014, checks, responses, errors, external, legacy, businessWrites, readOnly: true }, null, 2));
  await browser.close();
}
