/** Own-fixture settings/audit browser QA; --read-only repeats the optimized smoke. */
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const output = root + 'dev/local/';
const manifestPath = output + 'bff-homework-settings-audit-demo.json';
const demo = JSON.parse(await fs.readFile(manifestPath, 'utf8'));
const { password } = JSON.parse(await fs.readFile(output + 'accounts.json', 'utf8'));
const readOnly = process.argv.includes('--read-only');
const base = 'http://127.0.0.1:3001';
const checks = [], errors = [], external = [], legacy = [], writes = [];
let context, page, revision, cabinet;
let status = 'failed';
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const pass = name => { checks.push(name); console.log(JSON.stringify({ check: name })); };
const pagesInput = () => page.getByLabel('Количество страниц', { exact: true });

async function api(path) {
  assert(path.startsWith('/homework/'), 'Only readonly public homework calls');
  const response = await context.request.get(base + '/bff' + path, { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': revision } });
  assert.equal(response.status(), 200, path);
  return (await response.json()).data;
}
async function switchTo(organization, division) {
  const selector = page.getByLabel('Переключить кабинет и подразделение');
  await selector.waitFor();
  const value = `${organization}:${division}`;
  if (await selector.inputValue() !== value) {
    const loaded = page.waitForEvent('load');
    void loaded.catch(() => {});
    const changed = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/context' && response.request().method() === 'POST');
    await selector.selectOption(value);
    assert.equal((await changed).status(), 200);
    await loaded;
    await page.waitForLoadState('networkidle');
  }
  const me = await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
  assert.equal(me.status(), 200);
  revision = (await me.json()).context_revision;
}
async function login(name = 'dev_admin', organization = demo.organization_id, division = demo.division_a) {
  if (context) await context.close();
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await context.addInitScript(() => localStorage.setItem('flash_onboarding_seen', 'true'));
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { external.push(url.origin + url.pathname); return route.abort(); }
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/bff/unconnected')) legacy.push(url.pathname);
    if (!['GET', 'HEAD'].includes(request.method()) && !url.pathname.startsWith('/bff/auth/')) {
      if (readOnly || url.pathname !== '/bff/homework/settings' || request.method() !== 'PUT') return route.abort();
    }
    return route.continue();
  });
  page = await context.newPage();
  page.setDefaultTimeout(25000);
  page.on('request', request => { const url = new URL(request.url()); if (!['GET', 'HEAD'].includes(request.method()) && !url.pathname.startsWith('/bff/auth/')) writes.push(request.method() + ' ' + url.pathname); });
  page.on('pageerror', error => errors.push(error.message));
  page.on('dialog', dialog => dialog.accept());
  await page.goto(base + '/login');
  await page.getByLabel('Логин', { exact: true }).fill(name);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  const pending = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/login' && response.request().method() === 'POST');
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  const identity = await (await pending).json();
  assert(identity.status); cabinet = identity.user.role;
  await page.waitForURL('**/cabinet/**');
  await page.waitForLoadState('networkidle');
  await switchTo(organization, division);
  const cookie = (await context.cookies(base + '/bff/auth/me')).find(item => item.name === 'cpm_bff');
  assert(cookie?.httpOnly && cookie.sameSite === 'Strict');
  assert.equal(await page.evaluate(() => document.cookie.includes('cpm_bff=')), false);
}
async function open(section = 'assignments') {
  await page.goto(`${base}/cabinet/${cabinet}/${section}`);
  await page.locator('main').waitFor();
}
async function settings(section = 'assignments') {
  await open(section);
  await page.getByRole('button', { name: 'Настройки', exact: true }).click();
  await page.getByRole('heading', { name: 'Настройки домашних работ', exact: true }).waitFor();
  await pagesInput().waitFor();
}
async function mobile(image) {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(350);
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'Mobile page overflow');
  await page.screenshot({ path: output + image, fullPage: true });
  await page.setViewportSize({ width: 1440, height: 1000 });
}

try {
  await login();
  await settings();
  const source = await api('/homework/settings');
  assert.equal(await pagesInput().inputValue(), String(source.max_pages));
  assert.equal(await page.locator('main input[type="number"]').count(), 7);
  assert.equal(await pagesInput().isDisabled(), false);
  assert.equal(await page.getByRole('button', { name: 'Сохранить настройки', exact: true }).isDisabled(), true);
  pass('full administrator sees seven exact canonical settings and disabled unchanged save');
  await mobile('bff-homework-settings-mobile.png');
  pass('settings form remains usable without page overflow at 390px');

  if (!readOnly) {
    const next = source.max_pages + 1;
    await pagesInput().fill(String(next));
    await page.waitForFunction(async () => (await navigator.locks.query()).held.some(lock => lock.name === 'cpm-bff-context' && lock.mode === 'shared'));
    await page.getByLabel('Переключить кабинет и подразделение').selectOption(`${demo.organization_id}:${demo.division_b}`);
    await page.getByText(/Закройте редактор или проверку работы/).waitFor();
    assert.equal(await page.getByLabel('Переключить кабинет и подразделение').inputValue(), `${demo.organization_id}:${demo.division_a}`);
    pass('dirty settings hold the selected division through the shared context guard');
    const saved = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/homework/settings' && response.request().method() === 'PUT');
    await page.getByRole('button', { name: 'Сохранить настройки', exact: true }).click();
    assert.equal((await saved).status(), 200);
    await page.getByText('Настройки сохранены.', { exact: true }).waitFor();
    assert.equal((await api('/homework/settings')).max_pages, next);
    pass('one explicit UI PUT saves and rereads canonical limits for its own synthetic division');

    const lostValue = next + 1, before = writes.length;
    await pagesInput().fill(String(lostValue));
    let lost = false;
    await page.route('**/bff/homework/settings', async route => {
      assert.equal(route.request().method(), 'PUT');
      const response = await route.fetch(); assert.equal(response.status(), 200);
      lost = true; await route.abort('failed');
    }, { times: 1 });
    await page.getByRole('button', { name: 'Сохранить настройки', exact: true }).click();
    await page.getByText(/Исход сохранения неизвестен/).waitFor();
    assert(lost);
    assert.equal(writes.length, before + 1);
    assert(await page.getByRole('button', { name: 'Сохранить настройки', exact: true }).isDisabled());
    assert(await page.getByRole('button', { name: 'К заданиям', exact: true }).isDisabled());
    assert.equal(await page.evaluate(secret => Object.values(sessionStorage).some(value => value.includes(secret)), password), false);
    pass('committed lost PUT is marked unknown, blocks another save, and stores no password in its recovery journal');
    await page.reload();
    await page.getByRole('button', { name: 'Настройки', exact: true }).click();
    await page.getByText('Предыдущее сохранение подтверждено. Настройки прочитаны с сервера.', { exact: true }).waitFor();
    assert.equal(await pagesInput().inputValue(), String(lostValue));
    assert.equal(writes.length, before + 1);
    assert.equal(await page.evaluate(() => Object.keys(sessionStorage).some(key => key.startsWith('cpm-homework-settings-pending:'))), false);
    pass('reload recovers the committed settings through one canonical GET without resending PUT');
    demo.settings_a = await api('/homework/settings');
    await fs.writeFile(manifestPath, JSON.stringify(demo, null, 2) + '\n', { mode: 0o600 });
  }

  const beforeReadFailure = await pagesInput().inputValue();
  await page.route('**/bff/homework/settings', async route => {
    assert.equal(route.request().method(), 'GET');
    const response = await route.fetch(); assert.equal(response.status(), 200);
    await route.abort('failed');
  }, { times: 1 });
  await page.getByRole('button', { name: 'Обновить с сервера', exact: true }).click();
  await page.getByText('Не удалось получить ответ сервера. Повторите чтение.', { exact: true }).waitFor();
  assert.equal(await pagesInput().inputValue(), beforeReadFailure);
  await page.getByRole('button', { name: 'Обновить с сервера', exact: true }).click();
  await page.getByText('Настройки обновлены с сервера.', { exact: true }).waitFor();
  pass('failed settings refresh keeps prior values with a visible error and an explicit read retry');

  await page.getByRole('button', { name: 'К заданиям', exact: true }).click();
  await page.getByRole('button', { name: 'Аудит', exact: true }).click();
  await page.getByRole('heading', { name: 'Аудит домашних работ', exact: true }).waitFor();
  await page.locator('tbody tr').first().waitFor();
  assert.equal(await page.locator('tbody tr').count(), 50);
  const firstIDs = await page.locator('tbody tr td:first-child').allTextContents();
  assert.equal(Number(firstIDs[0]), demo.first_audit_id);
  const ownerAudit = (await api('/homework/audit?limit=200&after=0')).items;
  assert(ownerAudit.length >= 55 && ownerAudit.every(entry => entry.entity_id === String(demo.division_a)));
  pass('audit displays the real first 50 own-division records with account IDs and Moscow timestamps');
  await page.getByRole('button', { name: 'Следующая страница', exact: true }).click();
  await page.getByText('Страница 2', { exact: true }).waitFor();
  await page.getByText('Загрузка журнала…', { exact: true }).waitFor({ state: 'hidden' });
  const secondIDs = await page.locator('tbody tr td:first-child').allTextContents();
  assert.deepEqual(secondIDs.map(Number), ownerAudit.slice(50).map(entry => entry.id));
  assert(firstIDs.every(id => !secondIDs.includes(id)));
  assert(await page.getByRole('button', { name: 'Следующая страница', exact: true }).isDisabled());
  await page.getByRole('button', { name: 'Предыдущая страница', exact: true }).click();
  await page.getByText('Страница 1', { exact: true }).waitFor();
  await page.getByText('Загрузка журнала…', { exact: true }).waitFor({ state: 'hidden' });
  assert.deepEqual(await page.locator('tbody tr td:first-child').allTextContents(), firstIDs);
  pass('audit next and previous cursor pages have no skipped rows, duplicates or invented total');
  await mobile('bff-homework-audit-mobile.png');
  pass('audit scrolls inside its table without mobile page overflow');

  await switchTo(demo.organization_id, demo.division_b);
  await settings();
  assert.equal(await pagesInput().inputValue(), String(demo.settings_b.max_pages));
  assert.equal(await page.getByText(`Подразделение: ${demo.division_b_name}`, { exact: true }).count(), 1);
  pass('cabinet switch resets nested view and reads independent division B limits');

  for (const label of ['viewer', 'editor', 'monitor']) {
    await login(demo.users[label].login);
    await settings(label === 'monitor' ? 'monitoring' : 'assignments');
    assert.equal(await pagesInput().inputValue(), String(demo.settings_a.max_pages));
    assert(await pagesInput().isDisabled());
    assert.equal(await page.getByRole('button', { name: 'Сохранить настройки', exact: true }).count(), 0);
    const back = label === 'monitor' ? 'К мониторингу' : 'К заданиям';
    await page.getByRole('button', { name: back, exact: true }).click();
    assert.equal(await page.getByRole('button', { name: 'Аудит', exact: true }).count(), 0);
    pass(`${label} settings remain read-only and audit is hidden even with assignment edit rights`);
  }
  await login(demo.users.denied.login);
  await open();
  await page.getByRole('heading', { name: 'Раздел недоступен', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Настройки', exact: true }).count(), 0);
  pass('unrelated delegated role cannot open homework settings through the assignments screen');
  for (const label of ['student', 'proctor']) {
    await login(demo.users[label].login);
    await open('homework');
    assert.equal(await page.getByRole('button', { name: 'Настройки', exact: true }).count(), 0);
    assert.equal(await page.getByRole('button', { name: 'Аудит', exact: true }).count(), 0);
    assert.equal((await api('/homework/settings')).max_pages, demo.settings_a.max_pages);
    pass(`${label} has its allowed owner settings read without administrative mutation or audit actions`);
  }
  await login('dev_admin_math', demo.foreign_organization_id, demo.foreign_division);
  await settings();
  assert.equal(await pagesInput().inputValue(), String(demo.settings_foreign.max_pages));
  await page.getByRole('button', { name: 'К заданиям', exact: true }).click();
  await page.getByRole('button', { name: 'Аудит', exact: true }).click();
  await page.locator('tbody tr').waitFor();
  assert.equal(await page.locator('tbody tr').count(), 1);
  assert.equal(await page.locator('tbody tr td:last-child').textContent(), String(demo.foreign_division));
  pass('another organization sees only its own synthetic settings and single audit record');
  assert.deepEqual(external, []); assert.deepEqual(legacy, []); assert.deepEqual(errors, []);
  if (readOnly) assert.deepEqual(writes, []);
  else assert(writes.every(value => value === 'PUT /bff/homework/settings') && writes.length === 2);
  pass('all browser requests stay in localhost BFF with HttpOnly cookies and no legacy calls or unrelated writes');
  status = 'passed';
} catch (failure) {
  if (page) {
    console.error((await page.locator('body').innerText()).replaceAll(password, '[hidden]').slice(-6000));
    await page.screenshot({ path: output + 'bff-homework-settings-audit-failure.png', fullPage: true });
  }
  throw failure;
} finally {
  await fs.writeFile(output + `bff-homework-settings-audit-${readOnly ? 'optimized' : 'crud'}-browser-report.json`, JSON.stringify({ status, run: demo.run, check_count: checks.length, checks, errors, external, legacy, writes, readOnly }, null, 2) + '\n', { mode: 0o600 });
  await browser.close();
}
