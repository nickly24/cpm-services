// Real pending-retake + 5000-row preview on this runner's own public API fixtures.
// UI business writes are blocked. One explicit concurrent PUT edits only the
// owned import session to prove actual CAS; its original UUID is journaled.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { createHash, randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
const root = fileURLToPath(new URL('../../../', import.meta.url)), out = root + 'dev/local/', base = 'http://127.0.0.1:3001';
const manifest = out + 'bff-exams-delta-demo.json', demo = JSON.parse(await fs.readFile(manifest, 'utf8'));
const { password } = JSON.parse(await fs.readFile(out + 'accounts.json', 'utf8'));
assert(demo.readiness === 'ready' && demo.import_rows === 5000 && demo.prefix === `DEV Exams delta ${demo.run}`, 'Owned finalized fixtures are required');
assert(!demo.commands.some(c => c.status === 'pending'), 'Resolve the original uncertain command before browser QA');
assert(!demo.commands.some(c => c.method === 'PUT' && c.path === `/bff/exams/${demo.import_exam_id}/imports/questions/${demo.import_session_id}` && c.key !== '84c09342-1683-4fe7-8d02-9322a3bca95a' && c.status === 'confirmed'),
  'The one authorized CAS edit is already confirmed; reuse retained evidence or the readonly recovery runner');
const checks = [], errors = [], external = [], writes = [], legacy = [], importGets = [];
const sessions = [], closedContexts = new WeakSet();
let browser, page, status = 'failed', failure, concurrentCommand;
const pass = label => { checks.push(label); console.log(JSON.stringify({ check: label })); };
const save = async () => fs.writeFile(manifest, JSON.stringify(demo, null, 2) + '\n', { mode: 0o600 });
async function me(context) {
  const response = await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
  assert(response.status() === 200, 'Fresh identity unavailable'); return response.json();
}
async function get(context, path) {
  assert(path.startsWith('/exams/')); const identity = await me(context);
  const response = await context.request.get(base + '/bff' + path, { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': identity.context_revision } });
  assert(response.status() === 200, 'Owned public source read failed'); return (await response.json()).data;
}
async function login(name, role) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  sessions.push(context);
  await context.addInitScript(() => localStorage.setItem('flash_onboarding_seen', 'true'));
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url()), method = request.method();
    if (url.origin !== base) { external.push(url.origin + url.pathname); return route.abort(); }
    if (url.pathname.startsWith('/api/') || request.headers().authorization) { legacy.push(url.pathname); return route.abort(); }
    const auth = method === 'POST' && ['/bff/auth/login', '/bff/auth/context'].includes(url.pathname);
    if (method !== 'GET' && method !== 'HEAD' && !auth) { writes.push(method + ' ' + url.pathname); return route.abort(); }
    if (url.pathname === `/bff/exams/${demo.import_exam_id}/imports/questions/${demo.import_session_id}`) importGets.push({ offset: Number(url.searchParams.get('offset')), expected: url.searchParams.get('expected_revision') });
    return route.continue();
  });
  page = await context.newPage(); page.setDefaultTimeout(30000); page.on('pageerror', e => errors.push(e.message));
  await page.goto(base + '/login'); await page.getByLabel('Логин', { exact: true }).fill(name); await page.getByLabel('Пароль', { exact: true }).fill(password); await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await page.waitForURL('**/cabinet/**'); const selector = page.getByLabel('Переключить кабинет и подразделение'); await selector.waitFor();
  const value = `${demo.organization_id}:${demo.division_a}`;
  if (await selector.inputValue() !== value) {
    const loaded = page.waitForEvent('load'); void loaded.catch(() => {});
    const changed = page.waitForResponse(r => new URL(r.url()).pathname === '/bff/auth/context' && r.request().method() === 'POST');
    await selector.selectOption(value); assert((await changed).ok()); await loaded;
  }
  await page.waitForLoadState('networkidle');
  const cookie = (await context.cookies()).find(c => c.name === 'cpm_bff'); assert(cookie?.httpOnly && cookie.sameSite === 'Strict');
  return { context, page, role };
}
async function close(context) {
  if (closedContexts.has(context)) return;
  try {
    const identity = await me(context);
    const response = await context.request.post(base + '/bff/auth/logout', { headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': identity.context_revision }, data: {} });
    assert(response.status() === 200, 'Auth logout was not confirmed');
  } finally { await context.close(); closedContexts.add(context); }
}
async function fresh(page) { await page.waitForLoadState('networkidle'); assert((await page.getByRole('alert').allTextContents()).every(text => !text.trim()), 'UI displayed a source error'); }
try {
  browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  let session = await login(demo.student_login, 'student');
  const current = await get(session.context, `/exams/${demo.classic_exam_id}/results/${demo.student_account_id}`);
  assert(current.current.id === demo.first_attempt_id && current.current.status === 'completed' && current.current.effective_grade === 0 && current.current.has_active_retake && current.current.retake_status === 'pending_ready');
  await page.goto(base + '/cabinet/student/exams'); await page.getByRole('button', { name: 'Подробности', exact: true }).waitFor(); await fresh(page);
  await page.getByText('0 / 5', { exact: true }).waitFor(); await page.getByText('Пересдача ожидает готовности комиссии', { exact: true }).waitFor();
  pass('real student catalog retains completed zero and separately labels actual pending_ready retake');
  await page.goto(base + `/cabinet/student/exams?examId=${demo.classic_exam_id}`); await page.getByRole('heading', { name: 'Первая сдача', exact: true }).waitFor(); await fresh(page);
  await page.getByRole('button', { name: /Текущий результат.*0 \/ 5/ }).waitFor();
  await page.getByRole('status').filter({ hasText: 'До её завершения показан последний завершённый результат' }).waitFor();
  assert((await page.getByRole('button', { name: /Предыдущая сдача/ }).count()) === 0);
  assert((await page.getByRole('button', { name: 'История голосования', exact: true }).count()) === 0);
  await page.setViewportSize({ width: 390, height: 844 }); await page.waitForTimeout(350); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await page.screenshot({ path: out + 'bff-exams-delta-pending-mobile.png', fullPage: true });
  pass('real pending-retake detail preserves published grade and hides unfinished attempt/history/votes at390px');
  await close(session.context);
  session = await login('dev_admin', 'admin');
  const identity = await me(session.context), scope = `${identity.user.account_id}:${identity.user.organization_id}:${identity.user.division_id}:${identity.context_revision}`;
  const nativePath = `/exams/${demo.import_exam_id}/imports/questions/${demo.import_session_id}`;
  const first = await get(session.context, nativePath + '?limit=200&offset=0'), last = await get(session.context, nativePath + `?limit=200&offset=4800&expected_revision=${first.revision}`);
  assert(first.total === 5000 && first.summary.included_rows === 4999 && first.summary.error_rows === 0 && first.state === 'editable');
  assert(last.rows.at(-1).row_id === demo.import_last_row_id && last.rows.at(-1).excluded);
  await page.evaluate(({ key, id }) => sessionStorage.setItem(key, id), { key: `exam-import:${scope}:${demo.import_exam_id}:questions`, id: demo.import_session_id });
  await page.goto(base + `/cabinet/admin/exams?examId=${demo.import_exam_id}&tab=questions`);
  await page.getByRole('button', { name: 'Импорт .xlsx', exact: true }).click();
  const commit = page.getByRole('button', { name: 'Применить импорт целиком', exact: true }); await commit.waitFor(); await fresh(page);
  assert(await commit.isEnabled(), 'Valid completed preview remained unavailable');
  assert((await page.locator('body').innerText()).includes('Всего: 5000 · Включено: 4999 · Исключено: 1 · Ошибок: 0'));
  assert(importGets.length === 25 && importGets[0].expected === null && importGets.slice(1).every(p => p.expected === String(first.revision)));
  pass('real UI loads exact5000 rows through25 BFF pages pinned to first revision and renders full owner counts');
  let triggered = false, sideError, rejectSide;
  const sideFailure = new Promise((_, reject) => { rejectSide = reject; });
  void sideFailure.catch(() => {});
  await page.route('**/bff/exams/' + demo.import_exam_id + '/imports/questions/' + demo.import_session_id + '?*', async route => {
    const url = new URL(route.request().url());
    if (!triggered && url.searchParams.get('offset') === '200') {
      triggered = true;
      const row = last.rows.at(-1), body = { expected_revision: first.revision, changes: [{ row_id: row.row_id, excluded: true, input: { ...row.input, answerText: 'Synthetic answer updated during readonly CAS QA' } }] };
      const original = demo.commands.find(command => command.key === '84c09342-1683-4fe7-8d02-9322a3bca95a');
      assert(original?.status === 'rejected', 'Original known rejected receipt must remain retained');
      const digest = createHash('sha256').update(JSON.stringify(body)).digest('hex');
      assert(original.request_sha256 === digest, 'Original rejected body no longer matches the retained fixture');
      // The first command was definitively rejected by the Origin guard. Keep
      // its original key/payload and issue the separately authorized new edit.
      original.body = body;
      original.http_status = 403;
      concurrentCommand = { path: '/bff' + nativePath, method: 'PUT', key: randomUUID(), status: 'pending', request_sha256: digest, body };
      demo.commands.push(concurrentCommand); await save();
      try {
        const response = await session.context.request.put(base + concurrentCommand.path, { headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': identity.context_revision, 'Idempotency-Key': concurrentCommand.key }, data: body });
        concurrentCommand.http_status = response.status();
        if (response.status() >= 400 && response.status() < 500) { concurrentCommand.status = 'rejected'; await save(); }
        assert(response.status() === 200, 'Owned concurrent CAS command failed with HTTP ' + response.status());
        const changed = (await response.json()).data; assert(changed.revision === first.revision + 1);
        concurrentCommand.status = 'confirmed'; demo.import_revision = changed.revision; demo.import_validation_hash = changed.validation_hash; await save();
      } catch (error) { sideError = error; rejectSide(error); return route.abort(); }
    }
    return route.continue();
  });
  const conflict = page.waitForResponse(r => new URL(r.url()).pathname === '/bff' + nativePath && r.status() === 409);
  void conflict.catch(() => {});
  await page.getByRole('button', { name: 'Проверить сохранённое превью', exact: true }).click();
  await Promise.race([conflict, sideFailure]); await page.getByText(/Данные изменились\. Обновите страницу/).waitFor();
  if (sideError) throw sideError;
  assert(await commit.isDisabled());
  assert(await page.locator('textarea').first().isDisabled());
  pass('real concurrent own-session edit returns409 on pinned later page; retained preview cannot edit or commit');
  await page.getByRole('button', { name: 'Обновить данные', exact: true }).click(); await fresh(page); assert(await commit.isEnabled());
  const after = await get(session.context, nativePath + '?limit=200&offset=0'); assert(after.revision === first.revision + 1 && after.summary.included_rows === 4999);
  await page.screenshot({ path: out + 'bff-exams-delta-import5000.png', fullPage: true });
  pass('explicit fresh reload recovers current revision and full canonical counts without any import commit');
  await close(session.context);
  assert(errors.length === 0 && external.length === 0 && writes.length === 0 && legacy.length === 0);
  pass('readonly UI makes zero business writes/legacy/external calls; only one journaled synthetic concurrent CAS command');
  status = 'passed';
} catch (error) {
  failure = error instanceof Error ? error.message : 'browser failure'; process.exitCode = 1;
  if (page && !page.isClosed()) await page.screenshot({ path: out + 'bff-exams-delta-fixture-failure.png', fullPage: true }).catch(() => {});
  console.error('Owned Exams fixture browser failed; see private report');
} finally {
  for (const context of sessions) await close(context).catch(() => { errors.push('Auth cleanup not confirmed'); status = 'failed'; process.exitCode = 1; });
  await fs.writeFile(out + 'bff-exams-delta-fixture-browser-report.json', JSON.stringify({ status, checks, errors, external, writes, legacy, importPages: importGets, concurrentCommand, fixtureRun: demo.run, ...(failure ? { error: failure } : {}) }, null, 2) + '\n', { mode: 0o600 });
  await browser?.close(); console.log(JSON.stringify({ status, checks: checks.length }));
}
