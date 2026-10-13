// Read-only local Exams delta QA. Run only after the runtime owner's READY handoff.
// Auth login/context POSTs are allowed; all business writes are blocked.
// Pending retake and paginated import/CAS need separate fixtures and remain unit-only here.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const output = root + 'dev/local/';
const base = 'http://127.0.0.1:3001';
const XLSX = createRequire(root + 'cpm_front_new/package.json')('xlsx');
const demo = JSON.parse(await fs.readFile(output + 'bff-exams-demo.json', 'utf8'));
// Never log accounts.json, credentials, cookie values, auth headers or complete responses.
const { password } = JSON.parse(await fs.readFile(output + 'accounts.json', 'utf8'));
assert(demo.classic_exam_id && demo.outside_exam_id && demo.student_login, 'Existing API demo is required');
assert(typeof password === 'string' && password.length, 'Local fixture password is required');
const checks = [], errors = [], external = [], forbiddenWrites = [], legacy = [], bearer = [];
const observations = [], screenshots = [], templates = [], binaryDiagnostics = [];
const unitOnly = [
  'Pending-retake indicator/privacy during an unfinished retake: existing demo has a completed retake; no new assignment is created.',
  'First-revision import pagination, source/validation hash locking, malformed previews, owner metadata and CAS: no live import preview is created or mutated.',
];
let browser, activePage, status = 'failed', failure;
const pass = name => { checks.push(name); console.log(JSON.stringify({ check: name })); };
const equal = (actual, expected, message) => assert(JSON.stringify(actual) === JSON.stringify(expected), message);
const calendar = start => start ? new Date(Date.parse(start) + 3 * 3600000).toISOString().slice(0, 10) : null;
const dateLabel = value => value ? new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeZone: 'Europe/Moscow' }).format(new Date(value + 'T12:00:00+03:00')) : 'Не указано';

async function identity(context) {
  const response = await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
  assert(response.status() === 200, 'Authenticated local identity is unavailable');
  return response.json();
}
async function get(context, path) {
  assert(/^\/(exams(?:\/|\?|$)|core\/)/.test(path), 'Only owner read routes are permitted');
  const me = await identity(context);
  return context.request.get(base + '/bff' + path, { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': me.context_revision } });
}
async function read(context, path) {
  const response = await get(context, path);
  assert(response.status() === 200, `Owner GET failed (${response.status()}): ${path.split('?')[0]}`);
  const envelope = await response.json();
  assert(envelope.status === true && envelope.data !== undefined, 'Owner JSON envelope is invalid');
  return envelope.data;
}
async function all(context, path) {
  const rows = [];
  for (let offset = 0; offset <= 100000; offset += 200) {
    const page = await read(context, path + (path.includes('?') ? '&' : '?') + `limit=200&offset=${offset}`);
    assert(Array.isArray(page.items) && Number.isSafeInteger(page.total), 'Owner list envelope is invalid');
    rows.push(...page.items);
    if (rows.length >= page.total) return rows;
    assert(page.items.length > 0, 'Owner list changed during paging');
  }
  throw new Error('Owner catalogue exceeds the read-only harness limit');
}
async function login(loginName, role) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block', acceptDownloads: true });
  const requests = [];
  await context.addInitScript(() => localStorage.setItem('flash_onboarding_seen', 'true'));
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url()), method = request.method();
    requests.push({ method, path: url.pathname, query: url.search });
    if (request.headers().authorization) { bearer.push(url.pathname); return route.abort(); }
    if (url.origin !== base) { external.push(url.origin + url.pathname); return route.abort(); }
    if (url.pathname.startsWith('/api/')) { legacy.push(url.pathname); return route.abort(); }
    const authWrite = method === 'POST' && ['/bff/auth/login', '/bff/auth/context'].includes(url.pathname);
    if (!['GET', 'HEAD'].includes(method) && !authWrite) { forbiddenWrites.push(method + ' ' + url.pathname); return route.abort(); }
    return route.continue();
  });
  const page = await context.newPage(); activePage = page;
  page.setDefaultTimeout(25000);
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(base + '/login');
  await page.getByLabel('Логин', { exact: true }).fill(loginName);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await page.waitForURL('**/cabinet/**');
  const selector = page.getByLabel('Переключить кабинет и подразделение');
  await selector.waitFor();
  const cabinet = `${demo.organization_id}:${demo.division_a ?? demo.division_id}`;
  if (await selector.inputValue() !== cabinet) {
    const switched = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/context' && response.request().method() === 'POST');
    await selector.selectOption(cabinet);
    assert((await switched).status() === 200, 'Fixture cabinet switch failed');
    await page.waitForFunction(value => document.querySelector('[aria-label="Переключить кабинет и подразделение"]')?.value === value, cabinet);
  }
  const cookie = (await context.cookies(base + '/bff/auth/me')).find(item => item.name === 'cpm_bff');
  assert(cookie?.httpOnly && cookie.sameSite === 'Strict', 'Session cookie protection is missing');
  assert(!(await page.evaluate(() => document.cookie.includes('cpm_bff='))), 'Session cookie is visible to JavaScript');
  return { context, page, role, requests };
}
async function open(session, query, responsePath = '/bff/exams') {
  const from = session.requests.length;
  const received = session.page.waitForResponse(response => new URL(response.url()).pathname === responsePath && response.request().method() === 'GET');
  await session.page.goto(base + `/cabinet/${session.role}/exams` + (query ? '?' + query : ''));
  assert((await received).status() === 200, 'UI owner read failed');
  await session.page.waitForLoadState('networkidle');
  await session.page.waitForFunction(() => !document.body.innerText.includes('Загрузка…'));
  const alerts = await session.page.getByRole('alert').allTextContents();
  assert(alerts.every(text => !text.trim()), 'UI displayed an error alert');
  return session.requests.slice(from);
}
async function snapshot(page, filename) {
  await page.screenshot({ path: output + filename, fullPage: true });
  await fs.chmod(output + filename, 0o600); screenshots.push(filename);
}
async function mobile(page, filename) {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.waitForTimeout(350);
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), '390px mobile page overflows');
  await snapshot(page, filename);
  await page.setViewportSize({ width: 1440, height: 1000 });
}
function catalogueTraffic(requests, from, to, maxDirections, label) {
  const catalogs = requests.filter(request => request.path === '/bff/exams');
  assert(catalogs.length > 0, 'UI did not read the owner catalogue');
  for (const request of catalogs) {
    const query = new URLSearchParams(request.query);
    assert(query.get('date_from') === from && query.get('date_to') === to, 'UI date filters did not reach the owner before pagination');
    assert(query.get('limit') === '200' && Number(query.get('offset')) % 200 === 0, 'Unexpected owner catalogue pagination');
  }
  assert(!requests.some(request => /^\/bff\/exams\/\d+$/.test(request.path)), 'Catalogue caused per-exam detail reads');
  const directions = requests.filter(request => request.path === '/bff/core/directions').length;
  assert(directions <= maxDirections, 'Catalogue fetched directions more than once per adapter/filter source');
  observations.push({ label, catalogPages: catalogs.length, directionReads: directions, allowance: maxDirections, detailReads: 0 });
}
async function adminCatalog(session, sort, from, to, pageNumber = 1) {
  const owner = await all(session.context, `/exams?scope=division&sort=${sort}&date_from=${from}&date_to=${to}`);
  const requests = await open(session, new URLSearchParams({ scope: 'division', sort, dateFrom: from, dateTo: to, page: String(pageNumber) }).toString());
  const cards = session.page.locator('article').filter({ has: session.page.getByRole('button', { name: 'Открыть экзамен', exact: true }) });
  const expected = owner.slice((pageNumber - 1) * 20, pageNumber * 20);
  equal(await cards.locator('h2').allTextContents(), expected.map(row => row.name), 'Admin catalogue order differs from owner order');
  const text = await cards.allTextContents();
  for (let i = 0; i < expected.length; i++) {
    assert(text[i].includes(dateLabel(expected[i].exam_date)), 'Admin card does not display canonical exam_date');
    assert(text[i].includes(`ID ${expected[i].id}`), 'Admin card ID/order differs from owner');
    if (expected[i].exam_type === 'classic' && !expected[i].readiness) assert(text[i].includes('Не проверена'), 'Unchecked readiness was shown as incomplete');
  }
  // Admin filtering UI has its own direction lookup in addition to the catalogue adapter.
  catalogueTraffic(requests, from, to, 2, `admin:${sort}:page${pageNumber}`);
  pass(`admin ${sort} page ${pageNumber}: canonical dates/order, owner-side period, summary-only reads and unknown readiness`);
}
const templateHeaders = {
  questions: ['part_code', 'question_text', 'answer_text'],
  outside: ['student_id', 'student_login', 'points', 'grade', 'examinator'],
  assignments: ['student_id', 'student_login', ...Array.from({ length: 6 }, (_, index) => [`examinator_${index + 1}_id`, `examinator_${index + 1}_login`]).flat(), 'replacement_limit'],
};
async function template(session, kind, examId, tab) {
  await open(session, `examId=${examId}&tab=${tab}`, `/bff/exams/${examId}`);
  await session.page.getByRole('button', { name: 'Импорт .xlsx', exact: true }).click();
  const path = `/bff/exams/${examId}/imports/${kind}/template`;
  const received = session.page.waitForResponse(response => new URL(response.url()).pathname === path);
  const downloaded = session.page.waitForEvent('download');
  await session.page.getByRole('button', { name: 'Скачать шаблон Excel', exact: true }).click();
  const response = await received, download = await downloaded;
  assert(response.status() === 200, 'Protected template download failed');
  assert(response.headers()['content-type']?.split(';')[0] === 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', 'Template did not return XLSX MIME');
  assert(response.request().method() === 'GET' && !response.request().headers().authorization, 'Template download leaked a bearer or used a write');
  const filename = `bff-exams-delta-${kind}-template.xlsx`;
  await download.saveAs(output + filename); await fs.chmod(output + filename, 0o600);
  const bytes = await fs.readFile(output + filename);
  const native = await get(session.context, `/exams/${examId}/imports/${kind}/template`);
  assert(native.status() === 200 && bytes.equals(await native.body()), 'Actual browser file differs from protected owner download');
  binaryDiagnostics.push({ kind, bytes: bytes.length, leadingHex: bytes.subarray(0, 16).toString('hex'), encoding: response.headers()['content-encoding'] ?? null });
  assert(bytes.length > 0 && bytes[0] === 0x50 && bytes[1] === 0x4b, 'Template is not an Office ZIP package');
  const workbook = XLSX.read(bytes, { type: 'buffer' });
  assert(workbook.SheetNames.length === 1, 'Template must have exactly one worksheet');
  const rows = XLSX.utils.sheet_to_json(workbook.Sheets[workbook.SheetNames[0]], { header: 1, blankrows: false });
  equal(rows, [templateHeaders[kind]], 'Native template headers/data differ from owner contract');
  templates.push({ kind, filename, bytes: bytes.length, worksheetCount: 1, headerCount: rows[0].length });
  pass(`${kind}: real protected BFF XLSX download, one header-only worksheet, no JSON/bearer`);
}
async function studentCatalog(session, sort) {
  const catalogue = await all(session.context, `/exams?scope=division&sort=${sort}`), published = [];
  for (const exam of catalogue) {
    const rows = await all(session.context, `/exams/${exam.id}/results`);
    for (const row of rows.filter(row => exam.exam_type === 'outside_lms' || row.status === 'completed')) published.push({ exam, row });
  }
  const requests = await open(session, `sort=${sort}`);
  await session.page.getByText(`Результатов: ${published.length}`, { exact: true }).waitFor();
  const cards = session.page.locator('article').filter({ has: session.page.getByRole('button', { name: 'Подробности', exact: true }) });
  const expected = published.slice(0, 20);
  equal(await cards.locator('h2').allTextContents(), expected.map(item => item.exam.name), 'Student result ordering/name differs from owner catalogue');
  const text = await cards.allTextContents();
  for (let i = 0; i < expected.length; i++) {
    assert(text[i].includes(dateLabel(expected[i].row.exam_date)), 'Student result does not display canonical current date');
    assert(text[i].includes(`${expected[i].row.effective_grade ?? expected[i].row.grade} / 5`), 'Student effective grade differs from published owner result');
  }
  // Student results and the independent available-training adapter each read directions once.
  catalogueTraffic(requests, null, null, 2, `student:${sort}`);
  pass(`student ${sort}: real completed results, canonical date/order and read-only exam name`);
}

try {
  browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  const admin = await login('dev_admin', 'admin');
  const catalogue = await all(admin.context, '/exams?scope=division');
  const classicSummary = catalogue.find(row => row.id === demo.classic_exam_id), outsideSummary = catalogue.find(row => row.id === demo.outside_exam_id);
  assert(classicSummary && outsideSummary, 'Existing classic/outside fixtures are unavailable in the demo cabinet');
  const classic = await read(admin.context, `/exams/${demo.classic_exam_id}`), outside = await read(admin.context, `/exams/${demo.outside_exam_id}`);
  assert(Object.hasOwn(classicSummary, 'exam_date') && Object.hasOwn(classicSummary, 'start_at') && Object.hasOwn(classicSummary, 'end_at'), 'Classic summary lacks canonical date/window');
  equal(classicSummary.start_at, classic.exam_window?.start_at ?? null, 'Classic summary start differs from current division configuration');
  equal(classicSummary.end_at, classic.exam_window?.end_at ?? null, 'Classic summary end differs from current division configuration');
  equal(classicSummary.exam_date, calendar(classicSummary.start_at), 'Classic canonical calendar date differs from fixed UTC+03');
  equal(outsideSummary.exam_date, outside.date || null, 'Outside canonical date differs from current division date');
  const dates = [classicSummary.exam_date, outsideSummary.exam_date].filter(Boolean).sort();
  assert(dates.length === 2, 'Existing fixtures need calendar dates for the delta period checks');
  pass('real owner summaries contain current division dates/windows, independent of attempt snapshots');
  for (const sort of ['date_desc', 'date_asc', 'direction_asc', 'created_desc']) await adminCatalog(admin, sort, dates[0], dates.at(-1));
  await adminCatalog(admin, 'date_desc', dates[0], dates.at(-1), 2);
  await adminCatalog(admin, 'date_desc', classicSummary.exam_date, classicSummary.exam_date);
  await mobile(admin.page, 'bff-exams-delta-admin-mobile.png'); pass('admin delta catalogue fits 390px mobile viewport');
  await template(admin, 'questions', demo.classic_exam_id, 'questions');
  await template(admin, 'assignments', demo.classic_exam_id, 'assignments');
  await template(admin, 'outside', demo.outside_exam_id, 'results');
  await admin.context.close();

  const student = await login(demo.student_login, 'student');
  const me = await identity(student.context);
  assert(me.user.account_id === demo.student_account_id, 'Student demo account identity changed');
  const result = await read(student.context, `/exams/${demo.classic_exam_id}/results/${me.user.account_id}`);
  assert(result.current?.status === 'completed' && (result.current.effective_grade ?? result.current.grade) === 0, 'Existing published current zero is unavailable');
  assert(result.history?.some(row => row.status === 'completed'), 'Existing completed history is unavailable');
  assert(!Object.hasOwn(result.current, 'calculated_grade') && !Object.hasOwn(result.current, 'result_version'), 'Owner student result exposes private grading/CAS data');
  for (const sort of ['date_desc', 'date_asc', 'direction_asc']) await studentCatalog(student, sort);
  await mobile(student.page, 'bff-exams-delta-student-mobile.png'); pass('student delta results fit 390px mobile viewport');
  await open(student, `examId=${demo.classic_exam_id}`, `/bff/exams/${demo.classic_exam_id}/results/${me.user.account_id}`);
  await student.page.getByRole('heading', { level: 1, name: classic.name, exact: true }).waitFor();
  await student.page.getByRole('button', { name: /Текущий результат.*0 \/ 5/ }).waitFor();
  const previous = result.history.find(row => row.status === 'completed'), previousGrade = previous.effective_grade ?? previous.grade;
  await student.page.getByRole('button', { name: new RegExp(`Предыдущая сдача.*${previousGrade} / 5`) }).click();
  await student.page.getByRole('heading', { name: previous.attempt_no === 2 ? 'Пересдача' : 'Первая сдача', exact: true }).waitFor();
  assert(await student.page.getByRole('button', { name: 'История голосования', exact: true }).count() === 0, 'Student UI exposes examiner voting history');
  await snapshot(student.page, 'bff-exams-delta-student-history.png');
  pass('student real completed zero/history preserve exam name and hide private examiner votes/CAS');
  const denied = await get(student.context, `/exams/${demo.classic_exam_id}/imports/questions/template`);
  assert([403, 404].includes(denied.status()), 'Student accessed an administrative template');
  pass('protected template rejects student read access');
  await student.context.close();
  assert(errors.length === 0 && external.length === 0 && forbiddenWrites.length === 0 && legacy.length === 0 && bearer.length === 0, 'Browser errors or forbidden traffic were observed');
  pass('fresh protected sessions; no business writes, external/legacy traffic, bearer exposure or JavaScript errors');
  status = 'passed';
} catch (error) {
  failure = error instanceof Error ? error.message : 'Delta browser check failed';
  process.exitCode = 1;
  if (activePage && !activePage.isClosed()) {
    await snapshot(activePage, 'bff-exams-delta-failure.png').catch(() => {});
    await fs.writeFile(output + 'bff-exams-delta-failure.txt', await activePage.locator('body').innerText().catch(() => ''), { mode: 0o600 }).catch(() => {});
  }
  console.error('Exams delta browser check failed; see the private local report.');
} finally {
  try {
    await fs.writeFile(output + 'bff-exams-delta-report.json', JSON.stringify({ status, checks, unitOnly, observations, templates, binaryDiagnostics, screenshots, errors, external, forbiddenWrites, legacy, bearer, fixtureRun: demo.run, ...(failure ? { error: failure } : {}) }, null, 2) + '\n', { mode: 0o600 });
  } finally { await browser?.close(); }
  console.log(JSON.stringify({ status, checks: checks.length, report: 'dev/local/bff-exams-delta-report.json' }));
}
