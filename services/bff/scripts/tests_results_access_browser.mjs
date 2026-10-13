// U07 real canonical permissions; run only after isolated browser ACK/UI535READY.
// No owner, source, role or permission writes; the two-scope actor is API-owned.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
if (!process.argv.includes('--run')) { console.log(JSON.stringify({ status: 'pending_isolated_browser_ACK_UI535READY', liveChecks: 0 })); process.exit(0); }
const root = fileURLToPath(new URL('../../../', import.meta.url)), out = root + 'dev/local/', origin = 'http://127.0.0.1:3001';
const demo = JSON.parse(await fs.readFile(out + 'bff-tests-results-access-demo.json', 'utf8'));
const { password } = JSON.parse(await fs.readFile(out + 'accounts.json', 'utf8'));
assert(demo.readiness === 'ready' && demo.commands.length === 5 && demo.commands.every(command => command.status === 'confirmed'));
assert(demo.prefix.startsWith('DEV Results access ') && demo.source_fixture_run === '4629b1ae79');
const checks = [], errors = [], blocked = [], requests = [];
let browser, context, page, revision, status = 'failed', failure;
const reportPath = out + 'bff-tests-results-access-browser-report.json';
const pass = label => { checks.push(label); console.log(JSON.stringify({ check: label })); };
async function me() {
  const response = await context.request.get(origin + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
  assert(response.status() === 200); const value = await response.json(); revision = value.context_revision; return value;
}
async function read(path, expected = 200, pinned = revision) {
  assert(path.startsWith('/bff/tests/'));
  const response = await context.request.get(origin + path, { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': pinned } });
  assert.equal(response.status(), expected); return response.json();
}
async function switchDivision(division) {
  const selector = page.getByLabel('Переключить кабинет и подразделение'); await selector.waitFor();
  const expected = `${demo.organization_id}:${division}`;
  if (await selector.inputValue() !== expected) {
    const response = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/context' && response.request().method() === 'POST');
    await selector.selectOption(expected); assert((await response).status() === 200);
    await page.waitForLoadState('networkidle');
  }
  const value = await me();
  assert.equal(value.user.account_id, demo.actor.account_id);
  assert.equal(value.user.organization_id, demo.organization_id);
  assert.equal(value.user.division_id, division);
  return value;
}
try {
  browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await context.addInitScript(() => localStorage.setItem('flash_onboarding_seen', 'true'));
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== origin || url.pathname.startsWith('/api/') || request.headers().authorization) { blocked.push('external_legacy_or_bearer'); return route.abort(); }
    if (!['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(url.pathname)) { blocked.push('subject_write'); return route.abort(); }
    if (url.pathname.startsWith('/bff/')) requests.push({ method: request.method(), path: url.pathname, query: url.search });
    return route.continue();
  });
  page = await context.newPage(); page.setDefaultTimeout(25000); page.on('pageerror', error => errors.push(error.message));
  await page.goto(origin + '/login');
  await page.getByLabel('Логин', { exact: true }).fill(demo.actor.login);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await page.waitForURL('**/cabinet/staff_admin/**');
  const a = await switchDivision(demo.division_a);
  assert.deepEqual(a.user.permissions['test-results'], { view: true, edit: false });
  assert.equal(Object.hasOwn(a.user.permissions, 'test_results'), false);
  const cookie = (await context.cookies()).find(cookie => cookie.name === 'cpm_bff'); assert(cookie?.httpOnly && cookie.sameSite === 'Strict');
  await page.goto(origin + '/cabinet/staff_admin/test-results');
  await page.getByRole('heading', { name: 'Результаты тестов', exact: true }).waitFor();
  await page.getByText('Режим просмотра', { exact: true }).first().waitFor();
  assert.equal(await page.getByRole('link', { name: 'Тесты', exact: true }).count(), 0);
  await page.getByRole('link', { name: 'Результаты', exact: true }).waitFor();
  pass('canonical result view=true/edit=false shows allowed readonly section without tests catalogue rights');

  await page.getByLabel('Поиск ученика по имени, ID или классу', { exact: true }).fill(String(demo.student.account_id));
  await page.getByRole('button', { name: 'Открыть результаты: ' + demo.student.full_name, exact: true }).click();
  const external = page.getByRole('region', { name: 'Внешние результаты ученика', exact: true });
  await external.getByRole('heading', { name: 'Внешние тесты', exact: true }).waitFor();
  const zero = external.locator('tbody tr').filter({ hasText: demo.external_tests.zero.name });
  const ungraded = external.locator('tbody tr').filter({ hasText: demo.external_tests.ungraded.name });
  await zero.waitFor(); await ungraded.waitFor();
  assert.equal(await zero.getByRole('cell', { name: '0', exact: true }).count(), 1);
  assert.equal(await ungraded.getByRole('cell', { name: 'Балл не указан', exact: true }).count(), 1);
  assert.equal(await external.locator('tbody tr').count(), 2);
  for (const name of ['Изменить балл', 'Удалить результат', 'Повторить проверку', 'Сбросить сдачу', 'Удалить сдачу']) assert.equal(await page.getByRole('button', { name, exact: true }).count(), 0);
  assert.equal(await external.locator('input,textarea,form').count(), 0);
  const flatPath = `/bff/tests/external-results?account_id=${demo.student.account_id}&limit=100&offset=0`;
  const allowed = (await read(flatPath)).data;
  assert.equal(allowed.total, 2);
  assert(allowed.items.every(item => item.result.account_id === demo.student.account_id && item.test.division_id === demo.division_a));
  const pinnedA = revision;
  pass('real retained zero/null source rows use account541 and expose no correction/reset/import controls');

  await page.getByRole('button', { name: 'К списку учеников', exact: true }).click();
  const b = await switchDivision(demo.division_b);
  assert.deepEqual(b.user.permissions['test-results'], { view: false, edit: false });
  assert.equal(b.user.permissions.tests.view, true);
  assert.equal(Object.hasOwn(b.user.permissions, 'test_results'), false);
  const beforeDeniedPage = requests.length;
  await page.goto(origin + '/cabinet/staff_admin/test-results');
  await page.getByRole('heading', { name: 'Раздел недоступен', exact: true }).waitFor();
  assert.equal(await page.getByRole('link', { name: 'Результаты', exact: true }).count(), 0);
  assert.equal(await page.getByRole('heading', { name: 'Результаты тестов', exact: true }).count(), 0);
  assert.equal(await page.getByText(demo.student.full_name, { exact: true }).count(), 0);
  assert(!requests.slice(beforeDeniedPage).some(request => request.path === '/bff/tests/external-results' || request.path === '/bff/tests/submissions'));
  await read(flatPath, 403); await read('/bff/tests/submissions?limit=100&offset=0', 403);
  pass('same actor canonical view=false hides navigation and section, skips result requests and owner denies direct reads403');
  await read(flatPath, 409, pinnedA);
  await read('/bff/tests/external-tests/' + demo.external_tests.zero.id, 404);
  pass('old context409 and selected foreign-division target404 prevent A result reuse under B permissions');

  const back = await switchDivision(demo.division_a);
  assert.deepEqual(back.user.permissions['test-results'], { view: true, edit: false });
  await page.goto(origin + '/cabinet/staff_admin/test-results');
  await page.getByRole('heading', { name: 'Результаты тестов', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Открыть результаты: ' + demo.student.full_name, exact: true }).waitFor();
  assert.deepEqual((await read(flatPath)).data.items, allowed.items);
  pass('switch back restores same actor/org scoped readonly result rows from live owner');
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
  const screenshot = out + 'bff-tests-results-access-mobile.png'; await page.screenshot({ path: screenshot, fullPage: true }); await fs.chmod(screenshot, 0o600);
  assert(!errors.length && !blocked.length);
  pass('390px canonical result screen has no overflow, JS errors, legacy/external calls or subject writes');
  status = 'passed';
} catch (error) { failure = error.message; process.exitCode = 1; }
finally {
  if (context) {
    try { await me(); const logout = await context.request.post(origin + '/bff/auth/logout', { headers: { Origin: origin, 'X-CPM-Request': '1', 'X-CPM-Context': revision }, data: {} }); assert(logout.status() === 200); }
    catch { errors.push('Auth cleanup not confirmed'); status = 'failed'; process.exitCode = 1; }
    await context.close();
  }
  await browser?.close();
  await fs.writeFile(reportPath, JSON.stringify({ status, run: demo.run, count: checks.length, checks, errors, blocked, requests, subject_writes: 0,
    limitations: ['live CoreRole API creates canonical keys only; dual-alias precedence is unit8 evidence, not fabricated live permissions'],
    ...(failure ? { error: failure } : {}) }, null, 2) + '\n', { mode: 0o600 });
  console.log(JSON.stringify({ status, count: checks.length, report: reportPath }));
}
