/** Fresh public BFF Homework/PDF fixtures; readonly mode repeats published UI and class/export/monitoring checks. */
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import { createRequire } from 'node:module';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const root = new URL('../../../', import.meta.url).pathname, base = 'http://127.0.0.1:3001', storageOrigin = 'http://127.0.0.1:19000';
const readOnly = process.argv.includes('--read-only'), require = createRequire(import.meta.url);
const { PDFDocument } = require(root + 'cpm_front_new/node_modules/pdf-lib/cjs/index.js');
const XLSX = require(root + 'cpm_front_new/node_modules/xlsx/xlsx.js');
const { password } = JSON.parse(await fs.readFile(root + 'dev/local/accounts.json', 'utf8'));
const manifest = root + 'dev/local/bff-homework-delta-demo.json';
const checks = [], errors = [], external = [], legacy = [], writes = [], simulated = [];
let demo, context, page, revision, cabinet = 'admin', stage = 'browser', nativeMonitoring = null;
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const ok = name => { checks.push(name); console.log(JSON.stringify({ check: name })); };
function responseFor(path, method = 'GET') { const promise = page.waitForResponse(response => new URL(response.url()).pathname === path && response.request().method() === method); void promise.catch(() => {}); return promise; }
async function login(name = 'dev_admin') {
  if (context) await context.close();
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true });
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    if (![base, storageOrigin].includes(url.origin)) { external.push(url.origin + url.pathname); return route.abort(); }
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/bff/unconnected')) legacy.push(url.pathname);
    if (readOnly && !['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(url.pathname)) { writes.push({ path: url.pathname, method: request.method(), stage }); return route.abort(); }
    return route.continue();
  });
  page = await context.newPage(); page.setDefaultTimeout(30000); page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => dialog.accept());
  page.on('request', request => { const path = new URL(request.url()).pathname; if (!['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(path)) writes.push({ path, method: request.method(), stage }); });
  await page.goto(base + '/login'); await page.getByLabel('Логин', { exact: true }).fill(name); await page.getByLabel('Пароль', { exact: true }).fill(password);
  const pending = responseFor('/bff/auth/login', 'POST'); await page.getByRole('button', { name: 'Войти', exact: true }).click(); const identity = await (await pending).json(); assert(identity.status); revision = identity.context_revision; cabinet = identity.user.role;
  await page.waitForURL('**/cabinet/**'); await page.waitForLoadState('networkidle');
  if (demo?.division_id) {
    const selector = page.getByLabel('Переключить кабинет и подразделение'), value = `${demo.organization_id}:${demo.division_id}`;
    if (await selector.inputValue() !== value) { const loaded = page.waitForEvent('load'); void loaded.catch(() => {}); const changed = responseFor('/bff/auth/context', 'POST'); await selector.selectOption(value); assert((await changed).ok()); await loaded; await page.waitForLoadState('networkidle'); }
    revision = (await (await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } })).json()).context_revision;
  }
}
async function call(path, method = 'GET', body, expected = 200) {
  assert(!readOnly || method === 'GET');
  if (path === '/homework/settings' && method !== 'GET') await assertFixtureScope(context, revision);
  if (method !== 'GET') writes.push({ path: '/bff' + path, method, stage });
  const response = await context.request.fetch(base + '/bff' + path, { method, headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': revision }, ...(body === undefined ? {} : { data: body }) });
  assert.equal(response.status(), expected, `${method} ${path}`); return (await response.json()).data;
}
async function assertFixtureScope(scopeContext, expectedRevision) {
  assert(Number.isSafeInteger(demo?.division_id) && ![1001, 1002].includes(demo.division_id), 'Settings writes require an owned fresh fixture division');
  const identity = await scopeContext.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
  assert(identity.ok()); const value = await identity.json();
  assert.equal(value.user.organization_id, demo.organization_id);
  assert.equal(value.user.division_id, demo.division_id);
  assert.equal(value.context_revision, expectedRevision);
}
async function prepare() {
  stage = 'fixture'; await login(); const run = Date.now().toString(36), prefix = `DEV HW Delta ${run}`;
  demo = { run, prefix, organization_id: 1001 };
  demo.division_id = (await call('/core/divisions', 'POST', { name: prefix }, 201)).id;
  await page.reload(); await page.waitForLoadState('networkidle'); await login();
  for (const [key, role] of [['student', 'student'], ['supervisor', 'supervisor']]) {
    demo[key] = { full_name: prefix + ' ' + key, login: `hwdelta_${run}_${key}` };
    const created = await call('/core/users', 'POST', { ...demo[key], role, class: role === 'student' ? 10 : 0, password }, 201);
    demo[key].account_id = created.account_id; demo[key].profile_id = created.profile_id;
  }
  demo.type_id = (await call('/homework/types', 'POST', { name: `Delta ${run}` }, 201)).id;
  const deadline = new Date(Date.now() + 86400000).toISOString().slice(0, 10); demo.period = { dateFrom: deadline.slice(0, 8) + '01', dateTo: deadline.slice(0, 8) + String(new Date(Number(deadline.slice(0, 4)), Number(deadline.slice(5, 7)), 0).getDate()).padStart(2, '0') };
  demo.assignment_id = (await call('/homework/assignments', 'POST', { name: prefix + ' PDF', type_id: demo.type_id, deadline, published: true }, 201)).id;
  demo.zero_assignment_id = (await call('/homework/assignments', 'POST', { name: prefix + ' zero', type_id: demo.type_id, deadline, published: true }, 201)).id;
  await call(`/homework/assignments/${demo.zero_assignment_id}/manual`, 'POST', { account_id: demo.student.account_id, date_pass: new Date().toISOString().slice(0, 10), result: 0 });
  demo.settings = { ...(await call('/homework/settings')), max_source_bytes: 3 * 1048576, max_output_bytes: 1048576, max_pages: 2, dpi: 120, jpeg_quality: 75 };
  await call('/homework/settings', 'PUT', demo.settings);
  for (const count of [1, 3]) { const document = await PDFDocument.create(); for (let n = 0; n < count; n++) document.addPage([595, 842]).drawText(`Synthetic Homework Delta page ${n + 1}`); await fs.writeFile(root + `dev/local/bff-homework-delta-${count}.pdf`, await document.save()); }
  await fs.writeFile(root + 'dev/local/bff-homework-delta-source-large.pdf', Buffer.concat([await fs.readFile(root + 'dev/local/bff-homework-delta-1.pdf'), Buffer.alloc(4 * 1048576, ' ')]));
  await fs.writeFile(manifest, JSON.stringify(demo, null, 2)); stage = 'browser';
}
async function openWork() {
  await page.goto(`${base}/cabinet/student/homework?work=${demo.assignment_id}`); await page.getByRole('dialog').getByRole('heading', { name: demo.prefix + ' PDF', exact: true }).waitFor();
}
async function adminReducePolicy() {
  const admin = await browser.newContext();
  try {
    const headers = { Origin: base, 'X-CPM-Request': '1' };
    const logged = await admin.request.post(base + '/bff/auth/login', { headers, data: { login: 'dev_admin', password } }); assert(logged.ok()); let rev = (await logged.json()).context_revision;
    const selected = await admin.request.post(base + '/bff/auth/context', { headers: { ...headers, 'X-CPM-Context': rev }, data: { organization_id: demo.organization_id, division_id: demo.division_id } }); assert(selected.ok()); rev = (await selected.json()).context_revision;
    await assertFixtureScope(admin, rev);
    const response = await admin.request.put(base + '/bff/homework/settings', { headers: { ...headers, 'X-CPM-Context': rev }, data: { ...demo.settings, max_pages: 1 } }); assert(response.ok()); writes.push({ path: '/bff/homework/settings', method: 'PUT', stage: 'fixture-policy' });
  } finally { await admin.close(); }
}
async function uploadFlow() {
  await login(demo.student.login); await openWork(); const work = page.getByRole('dialog');
  await work.getByText(/Исходный PDF до 3 МиБ.*после обработки до 1 МиБ.*2 страниц/).waitFor();
  const before = writes.length; await work.locator('input[type=file]').setInputFiles(root + 'dev/local/bff-homework-delta-source-large.pdf'); await work.getByText(/Выберите PDF до 3 МиБ/).waitFor(); assert.equal(writes.length, before); ok('workspace rejects oversized source before upload using current3MiB policy');
  const accepted = responseFor(`/bff/homework/assignments/${demo.assignment_id}/uploads`, 'POST'); await work.locator('input[type=file]').setInputFiles(root + 'dev/local/bff-homework-delta-3.pdf'); const init = (await (await accepted).json()).data; assert.equal(init.job.limits.max_pages, 2); demo.failed_job_id = init.job.id;
  await work.getByText(/В PDF больше 2 страниц/).waitFor({ timeout: 60000 }); assert.equal((await call(`/homework/jobs/${init.job.id}`)).error_code, 'too_many_pages'); ok('real3pagePDF passes transfer then owner worker rejects accepted2page snapshot');
  await adminReducePolicy(); const retried = responseFor(`/bff/homework/jobs/${init.job.id}/retry`, 'POST'); await work.getByRole('button', { name: 'Повторить', exact: true }).click(); assert.equal((await (await retried).json()).data.limits.max_pages, 2);
  await work.getByText(/В PDF больше 2 страниц/).waitFor({ timeout: 60000 }); const retryJob = await call(`/homework/jobs/${init.job.id}`); assert.equal(retryJob.limits.max_pages, 2); assert.equal(retryJob.manual_attempts, 1); ok('retry keeps original2page accepted snapshot after current policy changed to1page');
  await work.getByRole('button', { name: 'Обновить работу', exact: true }).click(); await work.getByText(/не больше 1 страниц/).waitFor();
  const good = responseFor(`/bff/homework/assignments/${demo.assignment_id}/uploads`, 'POST'); await work.locator('input[type=file]').setInputFiles(root + 'dev/local/bff-homework-delta-1.pdf'); demo.ready_job_id = (await (await good).json()).data.job.id;
  await work.getByRole('heading', { name: 'Готово к отправке', exact: true }).waitFor({ timeout: 60000 }); const ready = await call(`/homework/jobs/${demo.ready_job_id}`); assert.equal(ready.status, 'ready'); assert.equal(ready.limits.max_pages, 1);
  const workspace = await call(`/homework/assignments/${demo.assignment_id}/workspace`); assert.equal(workspace.submission.draft_file.page_count, 1); assert(workspace.submission.draft_file.size_bytes <= 1048576); ok('valid1page PDF reaches real ready/draft with accepted1page limits and bounded output');
  const submitted = responseFor(`/bff/homework/assignments/${demo.assignment_id}/submit`, 'POST'); await work.getByRole('button', { name: 'Отправить на проверку', exact: true }).click(); assert((await submitted).ok()); await work.getByRole('heading', { name: 'Отправлено', exact: true }).waitFor(); ok('native ready PDF submits to review queue without an invented grade');
  await fs.writeFile(manifest, JSON.stringify(demo, null, 2));
}
async function reportAndMonitoring() {
  await login(demo.supervisor.login); const report = await call(`/homework/report?date_from=${demo.period.dateFrom}&date_to=${demo.period.dateTo}`); const student = report.students.find(item => item.account_id === demo.student.account_id); assert.equal(student.class, 10); assert.equal(student.results.find(item => item.assignment_id === demo.zero_assignment_id).result.result, 0);
  await page.goto(`${base}/cabinet/supervisor/homework`); await page.getByRole('button', { name: 'Диапазон дат', exact: true }).click(); await page.getByLabel('С', { exact: true }).fill(demo.period.dateFrom); await page.getByLabel('По', { exact: true }).fill(demo.period.dateTo); await page.getByRole('button', { name: 'Создать', exact: true }).click(); await page.getByLabel('Класс', { exact: true }).selectOption('10'); await page.getByText(demo.student.full_name, { exact: true }).waitFor();
  const download = page.waitForEvent('download'); await page.getByRole('button', { name: 'Excel', exact: true }).click(); const file = await download; const path = root + 'dev/local/bff-homework-delta-report.xlsx'; await file.saveAs(path); const workbook = XLSX.read(await fs.readFile(path)); const rows = XLSX.utils.sheet_to_json(workbook.Sheets[workbook.SheetNames[0]], { header: 1, defval: '' }); const row = rows.find(item => item[0] === demo.student.full_name); assert.equal(row[1], 10); assert(row.some(value => String(value).includes('0%'))); ok('report owner class10 and zero grade reach real UI class filter and XLSX');
  await page.setViewportSize({ width: 390, height: 844 }); await page.waitForTimeout(350); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await page.screenshot({ path: root + 'dev/local/bff-homework-delta-report-mobile.png' }); ok('class/report/export UI fits390px');
  await login(); await page.goto(`${base}/cabinet/admin/monitoring`); await page.getByRole('heading', { name: 'Состояние загрузок', exact: true }).waitFor(); const native = await call('/homework/monitoring'); assert.equal(native.organization_id, demo.organization_id); assert.equal(native.division_id, demo.division_id); assert.equal(typeof native.processing_ready, 'boolean');
  nativeMonitoring = { organization_id: native.organization_id, division_id: native.division_id, rabbitmq: native.rabbitmq, worker_state: native.worker?.state, storage_state: native.storage?.state, processing_ready: native.processing_ready };
  for (const label of ['Брокер Homework', 'Обработчик PDF', 'Хранилище PDF', 'Готовность обработки PDF']) await page.getByText(label, { exact: true }).waitFor(); ok('monitoring shows actual scoped counters plus native worker/storage/broker observations');
  await page.setViewportSize({ width: 390, height: 844 }); await page.waitForFunction(() => getComputedStyle(document.querySelector('.cabinet-main')).marginLeft === '0px'); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await page.screenshot({ path: root + 'dev/local/bff-homework-delta-monitoring-native-mobile.png', fullPage: true }); await page.setViewportSize({ width: 1440, height: 1000 });
  for (const state of ['unknown', 'stale']) {
    await page.route('**/bff/homework/monitoring', async route => { const response = await route.fetch(), body = await response.json(); Object.assign(body.data, { processing_ready: false, worker: { state, received_at: null, observed_at: null, rabbitmq: null, tools: null, processing: null, fresh_instances: 0 }, storage: { state, checked_at: null } }); await route.fulfill({ response, json: body }); });
    await page.getByRole('button', { name: 'Обновить', exact: true }).click(); await page.getByText(state === 'unknown' ? /Сигнал обработчика PDF пока не получен/ : /Сигнал обработчика PDF устарел/).waitFor(); await page.getByText('Не подтверждена', { exact: true }).waitFor(); simulated.push(state); await page.unroute('**/bff/homework/monitoring');
  }
  await page.route('**/bff/homework/monitoring', route => route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ status: false, error: 'upstream_unavailable' }) })); await page.getByRole('button', { name: 'Обновить', exact: true }).click(); await page.getByText(/Показан последний полученный ответ/).waitFor(); simulated.push('read_failure'); await page.unroute('**/bff/homework/monitoring'); ok('readonly browser-injected unknown/stale/read failure never claims healthy processing or empty success');
  await page.setViewportSize({ width: 390, height: 844 }); await page.waitForFunction(() => getComputedStyle(document.querySelector('.cabinet-main')).marginLeft === '0px'); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await page.screenshot({ path: root + 'dev/local/bff-homework-delta-monitoring-mobile.png' }); ok('native monitoring components fit390px');
  assert.deepEqual(errors, []); assert.deepEqual(external, []); assert.deepEqual(legacy, []); if (readOnly) assert.deepEqual(writes, []); ok(readOnly ? 'readonly Homework delta uses zero business writes and zero JS/external/legacy errors' : 'native PDF/report/monitoring use BFF and permitted signed storage with zero JS/external/legacy errors');
}
try {
  if (readOnly || process.argv.includes('--prepared')) demo = JSON.parse(await fs.readFile(manifest, 'utf8')); else await prepare();
  if (process.argv.includes('--prepare')) ok('fresh public BFF Homework delta fixtures prepared');
  else { if (!readOnly) await uploadFlow(); await reportAndMonitoring(); }
} catch (error) { console.error(error.stack); if (page) await page.screenshot({ path: root + 'dev/local/bff-homework-delta-failure.png', fullPage: true, mask: [page.locator('input[type=password]')] }); throw error; }
finally { if (demo && !readOnly) await fs.writeFile(manifest, JSON.stringify(demo, null, 2)); await fs.writeFile(root + `dev/local/bff-homework-delta-${readOnly ? 'optimized' : 'crud'}-browser-report.json`, JSON.stringify({ run: demo?.run, checks, errors, external, legacy, writes, readOnly, native_monitoring: nativeMonitoring, browser_simulated_observations: simulated }, null, 2)); await browser.close(); }
