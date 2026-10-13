/** Fresh Attendance zap delta via same-origin BFF. No camera, schedule, shared settings or owner URLs. */
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
const root = new URL('../../../', import.meta.url).pathname, base = 'http://127.0.0.1:3001';
const readOnly = process.argv.includes('--read-only'), prepared = process.argv.includes('--prepared');
const { password } = JSON.parse(await fs.readFile(root + 'dev/local/accounts.json', 'utf8'));
const manifest = root + 'dev/local/bff-attendance-delta-demo.json';
const checks = [], errors = [], external = [], legacy = [], writes = [], requestProof = [];
let demo, context, page, revision, cabinet = 'admin', stage = 'fixture';
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const ok = check => { if (!checks.includes(check)) checks.push(check); console.log(JSON.stringify({ check })); };
const save = async () => { if (demo && !readOnly) await fs.writeFile(manifest, JSON.stringify(demo, null, 2)); };
function responseFor(path, method = 'GET') { const pending = page.waitForResponse(response => new URL(response.url()).pathname === path && response.request().method() === method); void pending.catch(() => {}); return pending; }
async function login(name = 'dev_admin', selectFixture = true) {
  if (context) await context.close();
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { external.push(url.origin + url.pathname); return route.abort(); }
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/bff/unconnected')) { legacy.push(url.pathname); return route.abort(); }
    if (readOnly && !['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(url.pathname)) { writes.push({ method: request.method(), path: url.pathname, stage }); return route.abort(); }
    return route.continue();
  });
  page = await context.newPage(); page.setDefaultTimeout(20000); page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => {
    const path = new URL(request.url()).pathname;
    if (!['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(path)) {
      writes.push({ method: request.method(), path, stage });
      if (path.startsWith('/bff/attendance/')) requestProof.push({ method: request.method(), path, key: request.headers()['idempotency-key'], body: request.postData() });
    }
  });
  await page.goto(base + '/login'); await page.getByLabel('Логин', { exact: true }).fill(name); await page.getByLabel('Пароль', { exact: true }).fill(password);
  const pending = responseFor('/bff/auth/login', 'POST'); await page.getByRole('button', { name: 'Войти', exact: true }).click(); const identity = await (await pending).json(); assert(identity.status); revision = identity.context_revision; cabinet = identity.user.role;
  await page.waitForURL('**/cabinet/**'); await page.waitForLoadState('networkidle');
  if (selectFixture && demo?.division_id) {
    const selector = page.getByLabel('Переключить кабинет и подразделение'), value = `${demo.organization_id}:${demo.division_id}`;
    if (await selector.inputValue() !== value) { const loaded = page.waitForEvent('load'); void loaded.catch(() => {}); const changed = responseFor('/bff/auth/context', 'POST'); await selector.selectOption(value); assert((await changed).ok()); await loaded; await page.waitForLoadState('networkidle'); }
    const current = await identityNow(); assert.equal(current.user.organization_id, demo.organization_id); assert.equal(current.user.division_id, demo.division_id); revision = current.context_revision;
  }
}
async function identityNow() { const response = await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } }); assert(response.ok()); return response.json(); }
async function call(path, method = 'GET', data, expected = 200, key) {
  assert(!readOnly || method === 'GET');
  if (method !== 'GET') {
    assert(!demo.pending_command, 'Uncertain fixture command must be reconciled explicitly before another write');
    demo.pending_command = { method, path, key: key ?? null, data: data && { ...data, password: undefined } }; await save(); writes.push({ method, path: '/bff' + path, stage });
  }
  const response = await context.request.fetch(base + '/bff' + path, { method, headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': revision, ...(key ? { 'Idempotency-Key': key } : {}) }, ...(data === undefined ? {} : { data }) });
  const result = await response.json();
  if (method !== 'GET' && response.status() < 500) { demo.pending_command = null; await save(); }
  assert.equal(response.status(), expected, `${method} ${path} ${result.error ?? ''}`); return result.data ?? result;
}
const own = suffix => `/attendance/divisions/${demo.division_id}${suffix}`;
const read = suffix => call(own(suffix));
async function attendanceWrite(suffix, data, expected = 200, method = 'POST') {
  const snapshot = await read('/attendance-types'); return call(own(suffix), method, { expected_revision: snapshot.revision, data }, expected, randomUUID());
}
async function prepare() {
  try { const previous = JSON.parse(await fs.readFile(manifest, 'utf8')); assert(previous.completed, 'Existing incomplete fixture must be resumed with --prepared; do not recreate unknown commands'); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  const run = Date.now().toString(36), prefix = `DEV ATT Delta ${run}`; demo = { run, prefix, organization_id: 1001, dates: ['2037-03-01', '2037-03-02'], completed: false, pending_command: null }; await save(); await login();
  assert.equal((await identityNow()).user.organization_id, demo.organization_id);
  demo.division_id = (await call('/core/divisions', 'POST', { name: prefix }, 201)).id; assert(![1001, 1002].includes(demo.division_id)); await save(); await login();
  demo.roles = {};
  for (const [name, edit] of [['editor', true], ['viewer', false]]) demo.roles[name] = (await call('/core/roles', 'POST', { name: prefix + ' ' + name, permissions: { attendance: { view: true, edit }, zaps: { view: true, edit } } }, 201)).id;
  for (const [name, role] of [['student', 'student'], ['proctor', 'proctor'], ['editor', 'staff_admin'], ['viewer', 'staff_admin']]) {
    demo[name] = { login: `attdelta_${run}_${name}`, full_name: prefix + ' ' + name };
    const account = await call('/core/users', 'POST', { ...demo[name], password, role, class: role === 'student' ? 10 : 0, ...(role === 'staff_admin' ? { role_id: demo.roles[name] } : {}) }, 201); demo[name].account_id = account.account_id; await save();
  }
  // A second owned participation distinguishes the history guard from last_membership.
  demo.backup_division_id = (await call('/core/divisions', 'POST', { name: prefix + ' backup' }, 201)).id;
  await call(`/core/divisions/${demo.backup_division_id}/memberships/${demo.student.account_id}`, 'PUT', { role_id: null });
  demo.group_id = (await call('/core/groups', 'POST', { name: prefix + ' group' }, 201)).id;
  for (const name of ['student', 'proctor']) await call(`/core/groups/${demo.group_id}/members`, 'POST', { account_id: demo[name].account_id });
  const type = { color: '#23884c', sort_order: 10, is_active: true, counts_as_absent: false, scan_default: false };
  demo.present_type = (await attendanceWrite('/attendance-types', { ...type, code: 'delta_present', name: prefix + ' present', mark: 'П', counts_as_present: true, is_excused: false, zap_default: false })).value;
  demo.zap_type = (await attendanceWrite('/attendance-types', { ...type, code: 'delta_zap', name: prefix + ' excuse', mark: 'У', counts_as_present: false, is_excused: true, zap_default: true })).value;
  demo.days = [];
  for (const date of demo.dates) demo.days.push((await attendanceWrite('/class-days', { date, comment: prefix, lesson_ids: [] })).value);
  demo.foreign_login = JSON.parse(await fs.readFile(root + 'dev/local/attendance-scope-fixtures.json', 'utf8')).math_student.login;
  await save(); stage = 'browser'; ok('fresh division/accounts/proctor group/types/days prepared through BFF without shared settings');
}
async function open(section, heading) { await page.goto(`${base}/cabinet/${cabinet}/${section}`); await page.getByRole('heading', { name: heading, exact: true }).waitFor(); await page.getByText(/Загрузка (журнала|заявок)…/).waitFor({ state: 'hidden' }); await page.waitForLoadState('networkidle'); }
async function journal() { await open('attendance', 'Посещаемость'); await page.getByLabel('С даты', { exact: true }).fill(demo.dates[0]); await page.getByLabel('По дату', { exact: true }).fill(demo.dates[1]); await page.getByText('Загрузка журнала…', { exact: true }).waitFor({ state: 'hidden' }); await page.getByRole('button', { name: 'Обновить', exact: true }).click(); await page.waitForLoadState('networkidle'); }
const detail = () => page.getByRole('dialog', { name: 'Заявка на отгул', exact: true });
async function zapOpen() { await open('zaps', 'Отгулы'); const card = page.locator('article').filter({ hasText: demo.zap_text }); await card.getByRole('button', { name: 'Открыть заявку', exact: true }).click(); await detail().getByText('Загрузка заявки…', { exact: true }).waitFor({ state: 'hidden' }); }
const zapDetails = async () => (await read(`/zaps/${demo.zap_id}`)).value.zap;
const liveJournal = async () => (await read(`/attendance-report?date_from=${demo.dates[0]}&date_to=${demo.dates[1]}`)).value;
const liveEntry = data => data.entries.find(entry => entry.account_id === demo.student.account_id && entry.class_day_id === demo.days[0].id && !entry.is_deleted);
async function decision(status, lost = false) {
  await detail().getByLabel('Ответ ученику').fill('');
  const path = '/bff' + own(`/zaps/${demo.zap_id}/process`), label = status === 'approved' ? 'Одобрить отгул' : 'Отклонить отгул';
  if (!lost) { const pending = responseFor(path, 'POST'); await detail().getByRole('button', { name: label, exact: true }).click(); assert.equal((await pending).status(), 200); }
  else {
    let accepted, intercepted = false;
    await page.route('**' + path, async route => { if (route.request().method() !== 'POST' || intercepted) return route.continue(); intercepted = true; const response = await route.fetch(); assert(response.ok()); accepted = (await response.json()).data; await route.abort('failed'); });
    const before = requestProof.filter(item => item.path === path).length; await detail().getByRole('button', { name: label, exact: true }).click(); await detail().getByRole('button', { name: 'Повторить ту же команду', exact: true }).waitFor();
    const first = requestProof.filter(item => item.path === path).at(-1); demo.ui_pending_command = { ...first }; await save();
    assert(await detail().getByRole('button', { name: label, exact: true }).isDisabled());
    const replay = responseFor(path, 'POST'); await detail().getByRole('button', { name: 'Повторить ту же команду', exact: true }).click(); const repeated = (await (await replay).json()).data; assert.deepEqual(repeated, accepted);
    const sequence = requestProof.filter(item => item.path === path).slice(before); assert.equal(sequence.length, 2); assert(sequence[0].key); assert.equal(sequence[1].key, sequence[0].key); assert.equal(sequence[1].body, sequence[0].body); demo.exact_retry = { key: first.key, path, revision: accepted.revision }; demo.ui_pending_command = null; await save(); await page.unroute('**' + path);
  }
  await detail().getByText(status === 'approved' ? 'Одобрено' : 'Отклонено', { exact: true }).waitFor(); assert.equal((await zapDetails()).answer, '');
}
async function flows() {
  stage = 'browser'; await login(demo.proctor.login); await journal();
  const select = page.getByLabel(`Отметка ${demo.student.full_name} ${demo.dates[0]}`, { exact: true }); const marked = responseFor('/bff' + own(`/class-days/${demo.days[0].id}/attendance`), 'POST'); await select.selectOption(String(demo.present_type.id)); assert((await marked).ok()); demo.entry_id = liveEntry(await liveJournal()).id; await save(); ok('proctor own-group manual UI mark creates scoped entry');
  await login(demo.student.login); await open('zaps', 'Отгулы'); await page.getByRole('button', { name: 'Подать заявку', exact: true }).click();
  assert.equal(await detail().getByLabel('Причина', { exact: true }).getAttribute('maxlength'), '5000');
  await detail().getByRole('button', { name: /^Убрать дату / }).first().click();
  for (let offset = 0; offset < 60; offset++) { const date = new Date(Date.UTC(2037, 2, 1 + offset)).toISOString().slice(0, 10); await detail().getByLabel('Дата отгула', { exact: true }).fill(date); await detail().getByRole('button', { name: 'Добавить дату', exact: true }).click(); }
  await detail().getByText('Дат: 60 из 60', { exact: true }).waitFor(); await detail().getByLabel('Дата отгула', { exact: true }).fill('2037-05-01'); assert(await detail().getByRole('button', { name: 'Добавить дату', exact: true }).isDisabled());
  for (let offset = 59; offset >= 2; offset--) await detail().getByRole('button', { name: `Убрать дату ${new Date(Date.UTC(2037, 2, 1 + offset)).toISOString().slice(0, 10)}`, exact: true }).click();
  demo.zap_text = demo.prefix + ' manual correction'; await detail().getByLabel('Причина', { exact: true }).fill(demo.zap_text);
  const created = responseFor('/bff' + own('/zaps'), 'POST'); await detail().getByRole('button', { name: 'Отправить заявку', exact: true }).click(); const result = (await (await created).json()).data; demo.zap_id = result.value.id; await save(); await detail().waitFor({ state: 'hidden' }); ok('student UI uses5000text and60date boundary, then submits its own two-date request');
  await attendanceWrite('/zaps', { text: 'x'.repeat(5001), dates: [demo.dates[0]], images: [] }, 400);
  await attendanceWrite('/zaps', { text: 'invalid61dates', dates: Array.from({ length: 61 }, (_, i) => new Date(Date.UTC(2037, 2, 1 + i)).toISOString().slice(0, 10)), images: [] }, 400); ok('BFF owner rejects text5001 and61dates without creating requests');
  await login(demo.editor.login); await zapOpen(); assert.equal(await detail().getByLabel('Ответ ученику').getAttribute('maxlength'), '5000'); assert.equal(await detail().getByLabel('Ответ ученику').getAttribute('required'), null); await attendanceWrite(`/zaps/${demo.zap_id}/process`, { status: 'approved', answer: 'x'.repeat(5001) }, 400); ok('decision UI allows empty answer and owner rejects answer5001');
  await decision('approved'); let entry = liveEntry(await liveJournal()); assert.equal(entry.id, demo.entry_id); assert.equal(entry.type.id, demo.zap_type.id); assert.equal(entry.zap_id, demo.zap_id); ok('approval200 replaces manual mark with ZapDefault retaining the sameEntryID and empty answer');
  await decision('rejected'); assert.equal((await liveJournal()).entries.filter(item => !item.is_deleted).length, 0); ok('approved-to-rejected UI correction removes only linked live marks with empty answer');
  await decision('approved', true); entry = liveEntry(await liveJournal()); assert.equal(entry.id, demo.entry_id); assert.equal(entry.type.id, demo.zap_type.id); ok('rejected-to-approved lost reply replays exactkey/body/revision and returns the original receipt');
  for (const date of demo.dates) { const row = detail().locator('article').filter({ hasText: date }), pending = responseFor('/bff' + own(`/zaps/${demo.zap_id}/dates/${date}`), 'DELETE'); await row.getByRole('button', { name: 'Отвязать дату', exact: true }).click(); assert((await pending).ok()); await row.getByRole('button', { name: 'Повторить привязку даты', exact: true }).waitFor(); }
  const unlink = await liveJournal(); assert.equal(unlink.entries.filter(item => !item.is_deleted).length, 0); assert(unlink.days.every(day => day.has_history)); ok('unlink touches exact zap dates and preserves permanent class-day history');
  await detail().getByRole('button', { name: 'Закрыть заявку', exact: true }).click(); await journal();
  for (const date of demo.dates) assert(await page.locator('thead th').filter({ hasText: date }).getByRole('button', { name: 'Удалить день', exact: true }).isDisabled());
  await login(); assert.equal((await attendanceWrite(`/class-days/${demo.days[0].id}`, {}, 409, 'DELETE')).error, 'day_has_history');
  assert.equal((await call(`/core/divisions/${demo.division_id}/memberships/${demo.student.account_id}`, 'DELETE', undefined, 409)).error, 'membership_has_history'); assert.equal((await call(`/core/divisions/${demo.division_id}`, 'DELETE', undefined, 409)).error, 'division_has_members'); ok('permanent history blocks day/membership deletion and populated division remains protected');
  demo.completed = true; await save();
}
async function readonlyChecks() {
  stage = 'readonly'; await login(demo.viewer.login); await zapOpen(); await detail().getByText('Одобрено', { exact: true }).waitFor(); assert.equal(await detail().getByLabel('Ответ ученику').count(), 0); assert.equal(await detail().getByRole('button', { name: /Одобрить|Отклонить|Отвязать|Повторить привязку/ }).count(), 0); await detail().getByRole('button', { name: 'Закрыть заявку', exact: true }).click(); assert.equal(await page.getByRole('button', { name: 'Подать заявку', exact: true }).count(), 0); ok('staff view-only can inspect corrected zap but has no mutation controls');
  await journal(); assert.equal(await page.getByLabel(`Отметка ${demo.student.full_name} ${demo.dates[0]}`, { exact: true }).count(), 0); assert.equal(await page.getByRole('button', { name: /Создать день|Добавить тип/ }).count(), 0); ok('staff view-only journal exposes no editing controls');
  await login(demo.foreign_login, false); assert.equal((await identityNow()).user.organization_id, 2001);
  for (const suffix of [`/zaps/${demo.zap_id}`, '/students', `/class-days/${demo.days[0].id}/attendance`]) { const response = await context.request.get(base + '/bff' + own(suffix), { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': revision } }); assert.equal(response.status(), 404); } ok('foreign organization cannot read ownfixture zap/roster/day even with guessed IDs');
  await login(demo.editor.login); await zapOpen(); await page.setViewportSize({ width: 390, height: 844 }); await page.waitForFunction(() => getComputedStyle(document.querySelector('.cabinet-main')).marginLeft === '0px'); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await page.screenshot({ path: root + 'dev/local/bff-attendance-delta-mobile.png', fullPage: true }); assert(await page.getByLabel('Переключить кабинет и подразделение').count()); ok('corrected zap/empty-answer controls and cabinet switch fit390px');
  assert.deepEqual(errors, []); assert.deepEqual(external, []); assert.deepEqual(legacy, []); if (readOnly) assert.deepEqual(writes, []); ok(readOnly ? 'optimized readonly has zero business writes/JS/external/legacy calls' : 'Attendance delta uses only BFF with zero JS/external/legacy calls');
}
try {
  if (readOnly || prepared) { demo = JSON.parse(await fs.readFile(manifest, 'utf8')); assert(!demo.pending_command && !demo.ui_pending_command, 'Uncertain saved command requires explicit reconciliation'); } else await prepare();
  if (process.argv.includes('--prepare')) ok('fresh owned fixture prepared without shared writes');
  else { if (!readOnly && !demo.completed) await flows(); await readonlyChecks(); }
} catch (error) {
  console.error(error.stack); if (page) { console.error((await page.locator('body').innerText()).replaceAll(password, '[скрыт]').slice(-7000)); await page.screenshot({ path: root + 'dev/local/bff-attendance-delta-failure.png', fullPage: true, mask: [page.locator('input[type=password]')] }); } throw error;
} finally {
  await save(); await fs.writeFile(root + `dev/local/bff-attendance-delta-${readOnly ? 'optimized' : 'crud'}-browser-report.json`, JSON.stringify({ run: demo?.run, completed: demo?.completed, checks, errors, external, legacy, writes, readOnly, exact_retry: demo?.exact_retry ?? null, physical_camera: 'deferred; not exercised', scope: { organization_id: demo?.organization_id, division_id: demo?.division_id } }, null, 2)); await browser.close();
}
