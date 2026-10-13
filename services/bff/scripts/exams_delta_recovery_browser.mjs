// Read-only completion after the one authorized CAS edit was confirmed200.
// No new command keys, PUT, import commit or source fixture mutations.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
const root = fileURLToPath(new URL('../../../', import.meta.url)), out = root + 'dev/local/', base = 'http://127.0.0.1:3001';
const demo = JSON.parse(await fs.readFile(out + 'bff-exams-delta-demo.json', 'utf8'));
const { password } = JSON.parse(await fs.readFile(out + 'accounts.json', 'utf8'));
const reportPath = out + 'bff-exams-delta-fixture-browser-report.json';
const archive = out + 'bff-exams-delta-fixture-browser-after-cas-before-recovery.json';
const latest = JSON.parse(await fs.readFile(reportPath, 'utf8'));
const previous = latest.status === 'passed' ? JSON.parse(await fs.readFile(archive, 'utf8')) : latest;
assert(previous.fixtureRun === demo.run && previous.concurrentCommand?.status === 'confirmed' && previous.concurrentCommand.http_status === 200);
assert(previous.checks.length === 4 && !previous.errors.length && !previous.writes.length && !previous.external.length && !previous.legacy.length);
assert(!demo.commands.some(command => command.status === 'pending'));
const confirmed = demo.commands.find(command => command.key === previous.concurrentCommand.key);
assert(confirmed?.status === 'confirmed' && confirmed.http_status === 200 && confirmed.body.expected_revision + 1 === demo.import_revision);
if (latest.status !== 'passed') await fs.writeFile(archive, JSON.stringify(previous, null, 2) + '\n', { mode: 0o600 });
const checks = [...previous.checks], errors = [], recoveryPages = [], blocked = [];
let browser, context, page, revision, status = 'failed', failure;
const pass = label => { checks.push(label); console.log(JSON.stringify({ check: label })); };
async function me() {
  const response = await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
  assert(response.status() === 200); const identity = await response.json(); revision = identity.context_revision; return identity;
}
async function owner(path) {
  const identity = await me();
  const response = await context.request.get(base + '/bff' + path, { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': identity.context_revision } });
  assert(response.status() === 200); return (await response.json()).data;
}
async function ready() {
  await page.waitForFunction(() => [...document.querySelectorAll('button')].some(button => button.textContent?.trim() === 'Применить импорт целиком' && !button.disabled));
  assert((await page.getByRole('alert').allTextContents()).every(text => !text.trim()));
}
try {
  browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await context.addInitScript(() => localStorage.setItem('flash_onboarding_seen', 'true'));
  const path = `/exams/${demo.import_exam_id}/imports/questions/${demo.import_session_id}`;
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base || url.pathname.startsWith('/api/') || request.headers().authorization) { blocked.push('external_or_legacy'); return route.abort(); }
    if (!['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(url.pathname)) { blocked.push('subject_write'); return route.abort(); }
    if (url.pathname === '/bff' + path) recoveryPages.push({ offset: Number(url.searchParams.get('offset')), expected: url.searchParams.get('expected_revision') });
    return route.continue();
  });
  page = await context.newPage(); page.setDefaultTimeout(30000); page.on('pageerror', error => errors.push(error.message));
  await page.goto(base + '/login');
  await page.getByLabel('Логин', { exact: true }).fill('dev_admin');
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await page.waitForURL('**/cabinet/**');
  const selector = page.getByLabel('Переключить кабинет и подразделение'); await selector.waitFor();
  const selected = `${demo.organization_id}:${demo.division_a}`;
  if (await selector.inputValue() !== selected) {
    const switched = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/context' && response.request().method() === 'POST');
    await selector.selectOption(selected); assert((await switched).status() === 200);
    await page.waitForLoadState('networkidle');
  }
  const identity = await me(); assert(identity.user.division_id === demo.division_a);
  const first = await owner(path + '?limit=200&offset=0');
  assert(first.revision === demo.import_revision && first.revision === confirmed.body.expected_revision + 1);
  assert(first.total === 5000 && first.state === 'editable' && first.summary.included_rows === 4999 && first.summary.error_rows === 0);
  const scope = `${identity.user.account_id}:${identity.user.organization_id}:${identity.user.division_id}:${identity.context_revision}`;
  await page.evaluate(({ key, id }) => sessionStorage.setItem(key, id), { key: `exam-import:${scope}:${demo.import_exam_id}:questions`, id: demo.import_session_id });
  await page.goto(base + `/cabinet/admin/exams?examId=${demo.import_exam_id}&tab=questions`);
  await page.getByRole('button', { name: 'Импорт .xlsx', exact: true }).click(); await ready();
  assert((await page.locator('body').innerText()).includes('Всего: 5000 · Включено: 4999 · Исключено: 1 · Ошибок: 0'));
  assert(recoveryPages.length === 25 && recoveryPages[0].expected === null && recoveryPages.slice(1).every(item => item.expected === String(first.revision)));
  pass('fresh readonly recovery loads confirmed revision3 through25 pinned pages with exact5000 canonical counts');
  const completedReload = page.waitForResponse(response => {
    const url = new URL(response.url()); return url.pathname === '/bff' + path && url.searchParams.get('offset') === '4800' && url.searchParams.get('expected_revision') === String(first.revision) && response.status() === 200;
  });
  await page.getByRole('button', { name: 'Проверить сохранённое превью', exact: true }).click();
  await completedReload; await ready();
  const last = await owner(path + `?limit=200&offset=4800&expected_revision=${first.revision}`);
  assert(last.revision === first.revision && last.rows.at(-1).excluded && last.rows.at(-1).input.answerText === confirmed.body.changes[0].input.answerText);
  const screenshot = out + 'bff-exams-delta-import5000.png'; await page.screenshot({ path: screenshot, fullPage: true }); await fs.chmod(screenshot, 0o600);
  pass('explicit saved-preview reload completes current revision and enables valid preview without any import commit');
  assert(!errors.length && !blocked.length);
  pass('recovery makes zero source commands; the sole CAS edit retains confirmed original UUID/body and no unknown outcome');
  status = 'passed';
} catch (error) { failure = error.message; process.exitCode = 1; }
finally {
  if (context) {
    try { await me(); const logout = await context.request.post(base + '/bff/auth/logout', { headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': revision }, data: {} }); assert(logout.status() === 200); }
    catch { errors.push('Auth cleanup not confirmed'); status = 'failed'; process.exitCode = 1; }
    await context.close();
  }
  await browser?.close();
  await fs.writeFile(reportPath, JSON.stringify({ ...previous, status, checks, errors, recoveryPages, recoveryBlocked: blocked, recoverySubjectWrites: 0,
    ...(failure ? { error: failure } : { error: undefined }), retainedPriorFailureReport: archive }, null, 2) + '\n', { mode: 0o600 });
  console.log(JSON.stringify({ status, checks: checks.length, recoverySubjectWrites: 0 }));
}
