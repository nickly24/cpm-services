/** U04 only: public BFF fixtures, real post-commit loss and metadata-only recovery. */
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import { randomBytes, randomUUID } from 'node:crypto';

const root = new URL('../../../', import.meta.url).pathname;
const base = 'http://127.0.0.1:3001', output = root + 'dev/local/';
const manifestPath = output + 'bff-attendance-recovery-demo.json';
const reportPath = output + 'bff-attendance-recovery-report.json';
const prefix = 'cpm-bff-attendance-command:v1:';
if (!process.argv.includes('--run')) {
  console.log(JSON.stringify({ state: 'plan_only', browser_started: false, private_files_read: false, requires: 'Explicit coordinated --run authorization and actual owner/BFF/UI READY', normal_business_attempts: 27, exceptional_cleanup_ceiling: 29, scopes: 'Fresh org1001 divisions/roles/actors only', unknown_policy: 'Retain original UUID; no automatic resend or recreation' }, null, 2));
  process.exit(0);
}
const password = JSON.parse(await fs.readFile(output + 'accounts.json', 'utf8')).password;
const resumeTail = process.argv.includes('--resume-tail');
const prepared = process.argv.includes('--prepared') || resumeTail;
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const checks = [], errors = [], remote = [], legacy = [], requests = [];
let demo, context, page, adminContext, adminPage, stage = 'fixture', outcome;
const contexts = [];
const save = async () => { await fs.writeFile(manifestPath, JSON.stringify(demo, null, 2), { mode: 0o600 }); await fs.chmod(manifestPath, 0o600); };
const pass = (name, details = {}) => { checks.push({ name, ...details }); console.log('PASS ' + name); };
const relative = suffix => `/divisions/${demo.division_a}${suffix}`;
const own = suffix => '/attendance' + relative(suffix);
const journalKey = (actor = demo.editor, division = demo.division_a) => `${prefix}${actor.account_id}:${demo.organization_id}:${division}`;
const fullName = actor => `DEV U04 ${demo.run} ${actor.kind}`;
const roleBody = (actor, edit) => ({ name: `DEV U04 ${demo.run} ${actor.kind}`, permissions: Object.fromEntries(['attendance', 'zaps', 'schedule', 'scan'].map(name => [name, { view: true, edit }])) });
function cleanError(error) { return String(error?.message ?? error).split('Call log:')[0]; }
async function newContext() {
  const result = await browser.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' }); contexts.push(result);
  await result.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.origin !== base) { remote.push(url.origin + url.pathname); return route.abort(); }
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/bff/unconnected')) { legacy.push(url.pathname); return route.abort(); }
    return route.continue();
  });
  return result;
}
async function newPage(selectedContext) {
  const result = await selectedContext.newPage(); result.setDefaultTimeout(20000);
  result.on('pageerror', error => errors.push(cleanError(error)));
  result.on('request', request => {
    const path = new URL(request.url()).pathname;
    if (path.startsWith('/bff/')) requests.push({ method: request.method(), path, stage });
  });
  return result;
}
async function identity(selectedContext = context) {
  const response = await selectedContext.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } }); assert(response.ok()); return response.json();
}
async function changeContext(selectedContext, division) {
  const before = await identity(selectedContext);
  const response = await selectedContext.request.post(base + '/bff/auth/context', { headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': before.context_revision }, data: { organization_id: demo.organization_id, division_id: division } });
  assert(response.ok(), `Context switch ${response.status()}`);
}
async function login(selectedPage, selectedContext, loginName, division) {
  // The same context deliberately retains localStorage across logout/re-login.
  const me = await selectedContext.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
  if (me.ok()) {
    const previous = await me.json();
    const logout = await selectedContext.request.post(base + '/bff/auth/logout', { headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': previous.context_revision } });
    assert(logout.ok(), `Logout ${logout.status()}`);
  }
  await selectedPage.goto(base + '/login');
  await selectedPage.getByLabel('Логин', { exact: true }).fill(loginName); await selectedPage.getByLabel('Пароль', { exact: true }).fill(password);
  await selectedPage.getByRole('button', { name: 'Войти', exact: true }).click(); await selectedPage.waitForURL('**/cabinet/**');
  await selectedPage.getByLabel('Переключить кабинет и подразделение').waitFor();
  if (division) {
    const selector = selectedPage.getByLabel('Переключить кабинет и подразделение');
    const value = `${demo.organization_id}:${division}`;
    if (await selector.inputValue() !== value) { await selector.selectOption(value); await selectedPage.waitForLoadState('networkidle'); }
    const current = await identity(selectedContext); assert.equal(current.user.division_id, division);
  }
  await selectedPage.waitForLoadState('networkidle');
}
async function call(selectedContext, path, method = 'GET', data, expected = 200, key = randomUUID()) {
  const current = await identity(selectedContext);
  const response = await selectedContext.request.fetch(base + '/bff' + path, { method, headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': current.context_revision, ...(method === 'GET' ? {} : { 'Idempotency-Key': key }) }, ...(data === undefined ? {} : { data }) });
  const json = await response.json(); assert.equal(response.status(), expected, `${method} ${path}: ${response.status()} ${json.error ?? ''}`);
  return json.data ?? json;
}
async function fixtureWrite(path, method, data, expected = 201, revision = null) {
  assert(!demo.pending_fixture, 'Uncertain fixture command requires manual reconciliation; no new UUID');
  const entry = { key: randomUUID(), path, method, revision, state: 'pending' };
  demo.pending_fixture = entry; demo.fixture_commands.push(entry); await save();
  const result = await call(adminContext, path, method, data, expected, entry.key);
  entry.state = 'confirmed'; demo.pending_fixture = null; await save(); return result;
}
async function attendanceFixture(suffix, data) {
  const current = await call(adminContext, own('/attendance-types'));
  return fixtureWrite(own(suffix), 'POST', { expected_revision: current.revision, data }, 200, current.revision);
}
async function ownRole(actor, edit) {
  await fixtureWrite(`/core/roles/${actor.role_id}`, 'PUT', roleBody(actor, edit), 200);
  demo.permission_states ??= {}; demo.permission_states[actor.kind] = edit; await save();
}
async function prepare() {
  if (prepared) {
    demo = JSON.parse(await fs.readFile(manifestPath, 'utf8'));
    assert.equal(demo.state, 'ready', 'Only an explicitly ready fixture may be reused');
    assert(!demo.pending_fixture && !demo.pending_ui, 'Unknown original command must not be resent');
    await login(adminPage, adminContext, 'dev_admin', demo.division_a); return;
  }
  try { await fs.access(manifestPath); throw new Error('Retained fixture exists; inspect it and use --prepared only if ready. Never recreate an unknown command.'); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  await login(adminPage, adminContext, 'dev_admin'); const current = await identity(adminContext);
  demo = { run: randomBytes(4).toString('hex'), organization_id: current.user.organization_id, state: 'preparing', date: '2038-04-12', fixture_commands: [], pending_fixture: null, ui_commands: [], pending_ui: null };
  assert.equal(demo.organization_id, 1001); await save();
  demo.division_a = (await fixtureWrite('/core/divisions', 'POST', { name: `DEV U04 ${demo.run} A` })).id;
  demo.division_b = (await fixtureWrite('/core/divisions', 'POST', { name: `DEV U04 ${demo.run} B` })).id;
  assert(![1001, 1002].includes(demo.division_a)); await save();
  await adminPage.reload(); await adminPage.waitForLoadState('networkidle'); await changeContext(adminContext, demo.division_a); await adminPage.reload();
  for (const kind of ['editor', 'editor2', 'viewer']) {
    const actor = { kind, login: `u04_${demo.run}_${kind}` };
    actor.role_id = (await fixtureWrite('/core/roles', 'POST', roleBody(actor, kind !== 'viewer'))).id;
    actor.account_id = (await fixtureWrite('/core/users', 'POST', { login: actor.login, full_name: fullName(actor), password, role: 'staff_admin', class: 0, role_id: actor.role_id })).account_id;
    demo[kind] = actor; await save();
    await fixtureWrite(`/core/divisions/${demo.division_b}/memberships/${actor.account_id}`, 'PUT', { role_id: actor.role_id }, 200);
  }
  demo.student = { kind: 'student', login: `u04_${demo.run}_student` };
  demo.student.account_id = (await fixtureWrite('/core/users', 'POST', { login: demo.student.login, full_name: fullName(demo.student), password, role: 'student', class: 10 })).account_id; await save();
  await fixtureWrite(`/core/divisions/${demo.division_b}/memberships/${demo.student.account_id}`, 'PUT', { role_id: null }, 200);
  const type = { color: '#23884c', sort_order: 10, is_active: true, counts_as_absent: false, scan_default: false };
  demo.present_type = (await attendanceFixture('/attendance-types', { ...type, code: 'u04_present', name: `DEV U04 ${demo.run} present`, mark: 'П', counts_as_present: true, is_excused: false, zap_default: false })).value.id;
  demo.zap_type = (await attendanceFixture('/attendance-types', { ...type, code: 'u04_zap', name: `DEV U04 ${demo.run} excuse`, mark: 'У', counts_as_present: false, is_excused: true, zap_default: true })).value.id;
  demo.day_id = (await attendanceFixture('/class-days', { date: demo.date, comment: 'U04 synthetic recovery', lesson_ids: [] })).value.id;
  demo.foreign_login = JSON.parse(await fs.readFile(output + 'attendance-scope-fixtures.json', 'utf8')).math_student.login;
  demo.permission_states = { editor: true, editor2: true }; demo.state = 'ready'; await save(); pass('Fresh isolated Core actors/roles/cabinets and Attendance types/day via public BFF');
}
async function open(section, selectedPage = page) {
  const current = await identity(); await selectedPage.goto(`${base}/cabinet/${current.user.role}/${section}`);
  await selectedPage.getByRole('heading', { name: section === 'zaps' ? 'Отгулы' : 'Посещаемость', exact: true }).waitFor();
  await selectedPage.getByText(section === 'zaps' ? 'Загрузка заявок…' : 'Загрузка журнала…', { exact: true }).waitFor({ state: 'hidden' }); await selectedPage.waitForLoadState('networkidle');
}
async function journal(selectedPage = page) {
  await open('attendance', selectedPage); await selectedPage.getByLabel('С даты', { exact: true }).fill(demo.date); await selectedPage.getByLabel('По дату', { exact: true }).fill(demo.date);
  await selectedPage.getByRole('button', { name: 'Обновить', exact: true }).click(); await selectedPage.getByText('Загрузка журнала…', { exact: true }).waitFor({ state: 'hidden' }); await selectedPage.waitForLoadState('networkidle');
}
const detail = () => page.getByRole('dialog', { name: 'Заявка на отгул', exact: true });
const mark = selectedPage => selectedPage.getByLabel(`Отметка ${fullName(demo.student)} ${demo.date}`, { exact: true });
const postCount = path => requests.filter(request => request.path === path && request.method === 'POST' && request.stage !== 'fixture').length;
async function stored(actor = demo.editor, division = demo.division_a) { return page.evaluate(key => localStorage.getItem(key), journalKey(actor, division)); }
function validateStored(raw, captured) {
  const saved = JSON.parse(raw); assert.deepEqual(saved, captured); assert.deepEqual(Object.keys(saved).sort(), ['version', 'key', 'path', 'method', 'revision', 'account_id', 'organization_id', 'selected_division_id'].sort());
  assert(raw.length <= 2048); assert(!/qr|bearer|password|full_name|images|data|body|answer/i.test(raw)); return saved;
}
async function recordLoss(path, action) {
  assert(!demo.pending_ui, 'Original unknown UUID must be reconciled before another UI command');
  let resolveDropped, rejectDropped, captured;
  const dropped = new Promise((resolve, reject) => { resolveDropped = resolve; rejectDropped = reject; }); void dropped.catch(() => {});
  let first = true;
  const handler = async route => {
    if (route.request().method() !== 'POST' || !first) return route.fallback(); first = false;
    try {
      const request = route.request(), body = request.postDataJSON();
      const current = await identity();
      captured = { version: 1, key: request.headers()['idempotency-key'], path: path.slice('/bff/attendance'.length), method: 'POST', revision: body.expected_revision, account_id: current.user.account_id, organization_id: current.user.organization_id, selected_division_id: current.user.division_id };
      assert(captured.key); demo.pending_ui = captured; demo.ui_commands.push({ ...captured, state: 'unknown' }); await save();
      validateStored(await stored(current.user.account_id === demo.editor.account_id ? demo.editor : demo.editor2), captured);
      const response = await route.fetch(); assert.equal(response.status(), 200, 'Loss must follow a real accepted owner POST');
      const result = (await response.json()).data; assert.equal(result.revision, captured.revision + 1);
      await route.abort('failed'); resolveDropped({ captured, revision: result.revision });
    } catch (error) { rejectDropped(error); await route.abort('failed'); }
  };
  await page.route('**' + path, handler); const before = postCount(path);
  await action(); const accepted = await dropped; await page.getByRole('button', { name: 'Повторить ту же команду', exact: true }).first().waitFor();
  assert.equal(postCount(path), before + 1); return { ...accepted, before, path, remove: () => page.unroute('**' + path, handler) };
}
async function directWrite(path, actor, action, expected) {
  assert(!demo.pending_ui, 'Do not create another UI command while an original command is unknown');
  let complete, fail;
  const done = new Promise((resolve, reject) => { complete = resolve; fail = reject; }); void done.catch(() => {});
  const handler = async route => {
    if (route.request().method() !== 'POST') return route.fallback();
    try {
      const request = route.request(), body = request.postDataJSON(), current = await identity();
      const metadata = { version: 1, key: request.headers()['idempotency-key'], path: path.slice('/bff/attendance'.length), method: 'POST', revision: body.expected_revision, account_id: current.user.account_id, organization_id: current.user.organization_id, selected_division_id: current.user.division_id };
      validateStored(await stored(actor), metadata); demo.pending_ui = metadata; const command = { ...metadata, state: 'unknown' }; demo.ui_commands.push(command); await save();
      const response = await route.fetch(); assert.equal(response.status(), expected);
      if (response.status() >= 400) command.state = 'known_rejected'; else { const result = (await response.json()).data; assert.equal(result.revision, metadata.revision + 1); command.state = 'confirmed'; }
      demo.pending_ui = null; await save(); await route.fulfill({ response }); complete();
    } catch (error) { fail(error); await route.abort('failed'); }
  };
  await page.route('**' + path, handler); await action(); await done; await page.unroute('**' + path, handler);
}
async function confirmed(lost) {
  assert.equal(await stored(), null); assert.equal(demo.pending_ui.key, lost.captured.key);
  demo.ui_commands.find(command => command.key === lost.captured.key).state = 'confirmed_by_get'; demo.pending_ui = null; await save();
}
async function pureReload() {
  stage = 'reload-only'; await login(page, context, demo.editor.login, demo.division_a); await journal();
  const path = '/bff' + own(`/class-days/${demo.day_id}/attendance`);
  const lost = await recordLoss(path, () => mark(page).selectOption(String(demo.present_type)));
  await page.reload(); await journal(); validateStored(await stored(), lost.captured);
  assert.equal(await page.getByRole('button', { name: 'Повторить ту же команду', exact: true }).count(), 0);
  const receiptPath = '/bff/attendance/command-receipts/' + lost.captured.key;
  const receipt = page.waitForResponse(response => new URL(response.url()).pathname === receiptPath);
  await page.getByRole('button', { name: 'Проверить исход команды', exact: true }).click(); const proof = (await (await receipt).json()).data;
  assert.deepEqual(proof, { revision: lost.revision, value: { contract_version: 1, command_id: lost.captured.key, state: 'committed', organization_id: demo.organization_id, scope: 'division', division_ids: [demo.division_a], method: 'POST', path: lost.captured.path } });
  await page.getByText(/^Действие выполнено\./).waitFor(); await confirmed(lost);
  assert.equal(postCount(path), lost.before + 1); assert.equal(await mark(page).inputValue(), String(demo.present_type));
  await lost.remove(); await page.reload();
  pass('Real lost post-commit reply → reload → exact GET receipt → canonical refresh sends exactly one POST');
}
async function recovery() {
  stage = 'recovery'; await login(page, context, demo.editor.login, demo.division_a); await journal();
  const path = '/bff' + own(`/class-days/${demo.day_id}/attendance`);
  const lost = await recordLoss(path, () => mark(page).selectOption(String(demo.zap_type)));
  pass('Real owner POST commits once; dropped reply leaves bounded metadata before dispatch without body/QR');
  await ownRole(demo.editor, false);
  const denied = page.waitForResponse(response => new URL(response.url()).pathname === path && response.request().method() === 'POST');
  await page.getByRole('button', { name: 'Повторить ту же команду', exact: true }).click(); assert.equal((await denied).status(), 403);
  validateStored(await stored(), lost.captured); assert.equal(demo.pending_ui.key, lost.captured.key);
  await ownRole(demo.editor, true); pass('Unknown retry403 retains original UUID through live revoke and restoration of only the own role');
  const beforeLogout = await identity();
  assert((await context.request.post(base + '/bff/auth/logout', { headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': beforeLogout.context_revision } })).ok());
  const expired = page.waitForResponse(response => new URL(response.url()).pathname === path && response.request().method() === 'POST');
  await page.getByRole('button', { name: 'Повторить ту же команду', exact: true }).click(); assert.equal((await expired).status(), 401);
  await login(page, context, demo.editor.login, demo.division_a); await journal(); validateStored(await stored(), lost.captured);
  assert.equal(await page.getByRole('button', { name: 'Повторить ту же команду', exact: true }).count(), 0);
  pass('Retry401 plus same-storage re-login restores UUID and offers GET only, without rebuilding original inputs');
  const postsAfterDenials = postCount(path);
  const otherTab = await newPage(context); await journal(otherTab); assert(await mark(otherTab).isDisabled());
  const receiptPath = '/bff/attendance/command-receipts/' + lost.captured.key;
  const beforeReceipts = requests.filter(request => request.path === receiptPath).length;
  const beforeCabinetWrites = requests.filter(request => request.path.startsWith('/bff/attendance/') && !['GET', 'HEAD'].includes(request.method)).length;
  // Separate cached tabs with the same cookies can have different selected snapshots.
  // Auth context is changed by its public API to construct that concurrency case;
  // the ordinary header UI remains protected by its context guard.
  await changeContext(context, demo.division_b); await open('zaps', otherTab);
  const otherCreate = otherTab.getByRole('button', { name: 'Подать заявку', exact: true });
  if (!(await otherCreate.isDisabled())) {
    await otherCreate.click(); const otherForm = otherTab.getByRole('dialog', { name: 'Заявка на отгул', exact: true });
    await otherForm.getByLabel('Ученик').selectOption(String(demo.student.account_id));
    await otherForm.getByLabel('Причина', { exact: true }).fill('U04 blocked cross-cabinet attempt');
    const submit = otherForm.getByRole('button', { name: 'Отправить заявку', exact: true });
    if (!(await submit.isDisabled())) { await submit.click(); await otherForm.getByText(/В другом кабинете этой организации есть команда с неподтверждённым исходом/).first().waitFor(); }
  }
  assert.equal(await otherTab.getByRole('button', { name: 'Проверить исход команды', exact: true }).count(), 0);
  assert.equal(requests.filter(request => request.path === receiptPath).length, beforeReceipts); validateStored(await stored(), lost.captured);
  assert.equal(requests.filter(request => request.path.startsWith('/bff/attendance/') && !['GET', 'HEAD'].includes(request.method)).length, beforeCabinetWrites);
  assert.equal(await stored(demo.editor, demo.division_b), null);
  await changeContext(context, demo.division_a); await otherTab.close(); await journal();
  assert.equal(postCount(path), postsAfterDenials); pass('Same-storage second tab and different selected cabinet block overlapping writes without inspecting/clearing the original receipt');
  await login(page, context, demo.editor2.login, demo.division_a); await journal();
  assert.equal(await page.getByRole('button', { name: 'Проверить исход команды', exact: true }).count(), 0);
  await call(context, '/attendance/command-receipts/' + lost.captured.key, 'GET', undefined, 404); validateStored(await stored(), lost.captured);
  await login(page, context, demo.foreign_login); assert.equal((await identity()).user.organization_id, 2001);
  await call(context, '/attendance/command-receipts/' + lost.captured.key, 'GET', undefined, 404); validateStored(await stored(), lost.captured);
  pass('Foreign actor and organization GET are indistinguishable404 and cannot inspect or clear original actor storage');
  await login(page, context, demo.editor.login, demo.division_a); await journal();
  const corrupted = async route => { const response = await route.fetch(); assert.equal(response.status(), 200); const json = await response.json(); json.data.value.path = '/schedule/bulk'; await route.fulfill({ response, json }); };
  await page.route('**' + receiptPath, corrupted); await page.getByRole('button', { name: 'Проверить исход команды', exact: true }).click();
  await page.getByText('Ответ не подтверждает исход сохранённой команды. Журнал сохранён; новые действия заблокированы.', { exact: true }).waitFor(); validateStored(await stored(), lost.captured);
  await page.unroute('**' + receiptPath, corrupted); pass('Labelled corruption of a real native receipt is rejected; unknown UUID remains and no mutation is replayed');
  const receipt = page.waitForResponse(response => new URL(response.url()).pathname === receiptPath);
  await page.getByRole('button', { name: 'Проверить исход команды', exact: true }).click(); const proof = (await (await receipt).json()).data;
  assert.deepEqual(proof, { revision: lost.revision, value: { contract_version: 1, command_id: lost.captured.key, state: 'committed', organization_id: demo.organization_id, scope: 'division', division_ids: [demo.division_a], method: 'POST', path: lost.captured.path } });
  await page.getByText(/^Действие выполнено\./).waitFor(); await confirmed(lost); assert.equal(postCount(path), postsAfterDenials);
  await page.reload(); await journal(); assert.equal(await mark(page).inputValue(), String(demo.zap_type));
  await lost.remove(); pass('Reload recovery uses exact GET receipt + canonical journal refresh, with one accepted POST and no post-reload mutation');
}
async function createZap() {
  await open('zaps'); await page.getByRole('button', { name: 'Подать заявку', exact: true }).click();
  await detail().getByLabel('Ученик').selectOption(String(demo.student.account_id));
  await detail().getByLabel('Причина', { exact: true }).fill('U04 synthetic create latch');
}
async function latch() {
  stage = 'latch'; await createZap(); const path = '/bff' + own('/zaps');
  const lost = await recordLoss(path, () => detail().getByRole('button', { name: 'Отправить заявку', exact: true }).click());
  const failRefresh = route => route.request().method() === 'GET' ? route.abort('failed') : route.continue();
  await page.route('**' + path, failRefresh);
  await detail().getByRole('button', { name: 'Проверить исход команды', exact: true }).click();
  await detail().getByText(/^Действие выполнено\./).waitFor(); await confirmed(lost);
  assert.equal(await detail().count(), 1, 'Receipt must not invoke the old create callback with invented Value');
  const before = postCount(path); await detail().getByRole('button', { name: 'Отправить заявку', exact: true }).click();
  await detail().getByText('Действие уже выполнено. Закройте прежнюю форму и подтвердите намерение начать новое действие.', { exact: true }).waitFor(); assert.equal(postCount(path), before);
  assert.equal(await stored(), null); await page.unroute('**' + path, failRefresh);
  await detail().getByRole('button', { name: 'Закрыть', exact: true }).click(); await page.getByRole('button', { name: 'Понятно / начать новое действие', exact: true }).click();
  await lost.remove(); await open('zaps'); const zaps = await call(context, own('/zaps')); assert.equal(zaps.value.filter(zap => zap.text === 'U04 synthetic create latch').length, 1);
  pass('Real GET confirmation survives refresh failure and blocks duplicate submission in the unchanged open create form until explicit new intent');
}
async function knownAndStorage() {
  stage = 'known-denial'; await login(page, context, demo.editor2.login, demo.division_a); await createZap(); const path = '/bff' + own('/zaps');
  await page.evaluate(() => { window.__u04OriginalSetItem = Storage.prototype.setItem; Storage.prototype.setItem = function(key, value) { if (key.startsWith('cpm-bff-attendance-command:v1:')) throw new Error('synthetic storage failure'); return window.__u04OriginalSetItem.call(this, key, value); }; });
  const before = postCount(path); await detail().getByRole('button', { name: 'Отправить заявку', exact: true }).click();
  await detail().getByText(/Не удалось сохранить журнал команды/).waitFor(); assert.equal(postCount(path), before); assert.equal(await stored(demo.editor2), null);
  await page.evaluate(() => { Storage.prototype.setItem = window.__u04OriginalSetItem; delete window.__u04OriginalSetItem; });
  pass('Storage failure before dispatch prevents the owner POST and does not create or evict an unknown UUID');
  await ownRole(demo.editor2, false);
  await directWrite(path, demo.editor2, () => detail().getByRole('button', { name: 'Отправить заявку', exact: true }).click(), 403);
  await detail().getByText('Недостаточно прав в этом подразделении.', { exact: true }).waitFor(); assert.equal(await stored(demo.editor2), null);
  await ownRole(demo.editor2, true); pass('Known first direct403 clears only its own fresh command, unlike a denied retry of unknown');
  const failRefresh = route => route.request().method() === 'GET' ? route.abort('failed') : route.continue(); await page.route('**' + path, failRefresh);
  await directWrite(path, demo.editor2, () => detail().getByRole('button', { name: 'Отправить заявку', exact: true }).click(), 200);
  await detail().waitFor({ state: 'hidden' }); assert.equal(await stored(demo.editor2), null); await page.getByText('Сохранение подтверждено.', { exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Проверить исход команды', exact: true }).count(), 0); await page.unroute('**' + path, failRefresh);
  pass('Actual success followed by failed canonical refresh stays confirmed and does not offer retry/recovery');
}
async function missing() {
  stage = 'missing'; await login(page, context, demo.viewer.login, demo.division_a); await open('attendance');
  const snapshot = await call(context, own('/attendance-types'));
  const metadata = { version: 1, key: randomUUID(), path: relative(`/class-days/${demo.day_id}/attendance`), method: 'POST', revision: snapshot.revision, account_id: demo.viewer.account_id, organization_id: demo.organization_id, selected_division_id: demo.division_a };
  // A metadata-only storage fault models a request whose absence cannot be proven;
  // this UUID is never POSTed, and no accepted result is fabricated.
  demo.missing_receipt = metadata; await save(); await page.evaluate(({ key, value }) => localStorage.setItem(key, value), { key: journalKey(demo.viewer), value: JSON.stringify(metadata) });
  await page.reload(); await open('attendance'); const path = '/bff/attendance/command-receipts/' + metadata.key;
  const response = page.waitForResponse(result => new URL(result.url()).pathname === path); await page.getByRole('button', { name: 'Проверить исход команды', exact: true }).click(); assert.equal((await response).status(), 404);
  await page.getByText(/Исход команды пока невозможно подтвердить/).waitFor(); assert.equal(await stored(demo.viewer), JSON.stringify(metadata));
  assert.equal(await page.getByRole('button', { name: 'Повторить ту же команду', exact: true }).count(), 0); pass('Real missing404 preserves retained unknown metadata after reload; no body, mutation or absence claim');
}
try {
  adminContext = await newContext(); adminPage = await newPage(adminContext); await prepare();
  context = await newContext(); page = await newPage(context);
  if (resumeTail) {
    const previous = JSON.parse(await fs.readFile(reportPath, 'utf8'));
    assert.equal(previous.status, 'failed'); assert.equal(previous.stage, 'latch'); assert.equal(previous.check_count, 9); assert.equal(previous.fixture.run, demo.run);
    assert.equal(previous.pending_retained, false); assert.deepEqual(previous.cleanup.errors, []); assert.equal(previous.cleanup.auth_closed.length, 2);
    assert(demo.ui_commands.every(command => command.state === 'confirmed_by_get')); assert(!demo.pending_ui && !demo.pending_fixture);
    await fs.copyFile(reportPath, output + 'bff-attendance-recovery-first-attempt-report.json');
    checks.push(...previous.checks); requests.push(...previous.requests);
    await login(page, context, demo.editor.login, demo.division_a);
  } else { await pureReload(); await recovery(); }
  await latch(); await knownAndStorage(); await missing();
  assert.deepEqual(remote, []); assert.deepEqual(legacy, []); assert.deepEqual(errors, []);
  pass('All probes stayed on UI/BFF, with zero remote/legacy calls and zero browser exceptions');
  demo.state = 'passed'; demo.completed_at = new Date().toISOString(); await save();
  outcome = { status: 'passed', checks, check_count: checks.length, errors, remote, legacy, requests, fixture: { run: demo.run, division_a: demo.division_a, division_b: demo.division_b }, ...(resumeTail ? { resumed_from: 'bff-attendance-recovery-first-attempt-report.json', initial_checks: 9 } : {}), limitations: ['Old bulk409 is covered by boundary unit tests only; no native fixture was invented.', 'Missing404 metadata is deliberately retained as UNKNOWN; no body or original write was sent.'] };
} catch (error) {
  await page?.screenshot({ path: output + 'bff-attendance-recovery-failure.png', fullPage: true }).catch(() => {});
  outcome = { status: 'failed', stage, error: cleanError(error), checks, check_count: checks.length, errors, remote, legacy, requests, fixture: demo && { run: demo.run, division_a: demo.division_a, division_b: demo.division_b }, pending_retained: !!(demo?.pending_fixture || demo?.pending_ui) };
  console.error(cleanError(error)); process.exitCode = 1;
} finally {
  const cleanup = { permissions_restored: [], auth_closed: [], errors: [] };
  // Only restore known revoked own roles. Never replay or replace an uncertain
  // fixture command in order to clean up an Auth or business outcome.
  if (demo && !demo.pending_fixture) {
    for (const actor of [demo.editor, demo.editor2].filter(Boolean)) {
      if (demo.permission_states?.[actor.kind] === false) {
        try { await ownRole(actor, true); cleanup.permissions_restored.push(actor.role_id); }
        catch (error) { cleanup.errors.push(cleanError(error)); }
      }
    }
  }
  for (const [index, selectedContext] of contexts.entries()) {
    for (const selectedPage of selectedContext.pages()) await selectedPage.unrouteAll({ behavior: 'wait' }).catch(error => cleanup.errors.push(cleanError(error)));
    try {
      const me = await selectedContext.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
      if (me.ok()) {
        const current = await me.json();
        const logout = await selectedContext.request.post(base + '/bff/auth/logout', { headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': current.context_revision } });
        assert(logout.ok(), `Cleanup logout ${logout.status()}`); cleanup.auth_closed.push({ context: index, account_id: current.user.account_id, organization_id: current.user.organization_id, status: logout.status() });
      } else { assert.equal(me.status(), 401); cleanup.auth_closed.push({ context: index, already_closed: true }); }
    } catch (error) { cleanup.errors.push(cleanError(error)); }
    await selectedContext.close().catch(error => cleanup.errors.push(cleanError(error)));
  }
  await browser.close();
  outcome ??= { status: 'failed', error: 'No run outcome', checks, check_count: checks.length }; outcome.cleanup = cleanup;
  if (demo) {
    const core = demo.fixture_commands.filter(command => command.path.startsWith('/core/')).length;
    const attendanceFixtures = demo.fixture_commands.filter(command => command.path.startsWith('/attendance/')).length;
    const ui = requests.filter(request => request.path.startsWith('/bff/attendance/') && !['GET', 'HEAD'].includes(request.method) && request.stage !== 'fixture').length;
    outcome.business_attempts = { core, attendance_fixtures: attendanceFixtures, attendance_ui: ui, total: core + attendanceFixtures + ui };
    outcome.command_proof = demo.ui_commands.map(command => ({ key: command.key, method: command.method, path: command.path, expected_revision: command.revision, account_id: command.account_id, organization_id: command.organization_id, selected_division_id: command.selected_division_id, state: command.state }));
    outcome.qr_scope = 'No scan/QR business command was sent; QR/body exclusion is covered by source and boundary tests, not by a live QR-loss claim.';
  }
  if (cleanup.errors.length) { outcome.status = 'failed'; process.exitCode = 1; }
  if (demo) { demo.auth_closed = cleanup.auth_closed.length === contexts.length && cleanup.errors.length === 0; await save(); }
  await fs.writeFile(reportPath, JSON.stringify(outcome, null, 2));
}
