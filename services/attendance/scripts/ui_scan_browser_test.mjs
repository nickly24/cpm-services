// Read-only scan day-picker/table/XLSX acceptance after the UI owner says READY2.
// Only fresh Auth sessions change; no QR generation, scans, camera or domain writes.
// Never print Playwright error messages, passwords, tokens, login DOM or storage.
import fs from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';
import {fileURLToPath} from 'node:url';
import {createRequire} from 'node:module';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const output = path.join(root, 'dev/local');
const base = 'http://127.0.0.1:3001';
const prefix = 'attendance-ui-scan';
const args = process.argv.slice(2);
if (args.includes('--help')) {
  console.log('After optimized UI3001 READY2 and BFF8001 owner window: node services/attendance/scripts/ui_scan_browser_test.mjs --run');
  console.log('Uses existing own Attendance scope/scan fixtures, isolated dev_admin Auth, three real XLSX downloads. Domain writes are blocked.');
  process.exit(0);
}
if (args.length !== 1 || args[0] !== '--run') {
  console.error('Use --help, or --run only after the BFF/UI owner execution window is agreed.');
  process.exit(2);
}

const checks = [], errors = [], external = [], blockedWrites = [], artifacts = [], diagnostics = [];
const authenticated = new Set();
const safeErrorName = error => ['Error', 'AssertionError', 'TimeoutError', 'TypeError', 'RangeError', 'SyntaxError'].includes(error?.name) ? error.name : 'Error';
let stage = 'read-private-fixtures', failure, reportSaved = false;
let browser, context, page, scope, fixture, password, XLSX;

async function screenshot(tag) {
  if (!page || !authenticated.has(page) || !new URL(page.url()).pathname.startsWith('/cabinet/')) return;
  const filename = `${prefix}-${tag}.png`;
  await page.screenshot({path: path.join(output, filename), fullPage: true});
  artifacts.push(filename);
}

async function controls(tag) {
  if (!page || !authenticated.has(page) || !new URL(page.url()).pathname.startsWith('/cabinet/')) return;
  const values = await page.evaluate(() => {
    const labelText = element => [...(element.closest('label')?.childNodes ?? [])]
      .filter(node => node.nodeType === Node.TEXT_NODE).map(node => node.textContent).join('').trim();
    const safeID = value => /^(?:all|none|\d+(?::\d*)?)$/.test(value) ? value : '<non-id>';
    const selects = [...document.querySelectorAll('select')], result = {};
    for (const [key, label] of [['auth_context', 'Переключить кабинет и подразделение'], ['division', 'Подразделение журнала'], ['day', 'День посещения']]) {
      const matches = selects.filter(element => element.getAttribute('aria-label') === label || labelText(element) === label);
      result[key] = {matches: matches.length, controls: matches.map(element => ({
        selected_id: safeID(element.value), disabled: element.disabled,
        option_ids: [...element.options].map(option => safeID(option.value)),
        ...(key === 'day' ? {option_dates: [...element.options].map(option => /^\d{4}-\d{2}-\d{2}$/.test(option.text) ? option.text : '<non-date>')} : {})
      }))};
    }
    for (const [key, label] of [['date_from', 'С даты'], ['date_to', 'По дату']]) {
      const matches = [...document.querySelectorAll('input[type="date"]')].filter(element => labelText(element) === label);
      result[key] = matches.map(element => /^\d{4}-\d{2}-\d{2}$/.test(element.value) ? element.value : '<non-date>');
    }
    return result;
  });
  diagnostics.push({stage: tag, controls: values});
}

async function login() {
  stage = 'admin-login/create-isolated-session';
  context = await browser.newContext({viewport: {width: 1440, height: 1000}, acceptDownloads: true});
  page = await context.newPage();
  page.setDefaultTimeout(25000);
  page.on('pageerror', error => errors.push({stage, type: safeErrorName(error)}));
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url()), method = request.method();
    if (url.protocol === 'http:' || url.protocol === 'https:') {
      if (url.hostname !== '127.0.0.1') {
        external.push(url.hostname);
        return route.abort();
      }
      const auth = ['/bff/auth/login', '/bff/auth/context', '/bff/auth/refresh', '/bff/auth/logout'];
      if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && !auth.includes(url.pathname)) {
        blockedWrites.push({method, path: url.pathname});
        return route.abort();
      }
    }
    return route.continue();
  });
  stage = 'admin-login/open-login';
  await page.goto(base + '/login');
  stage = 'admin-login/fill-private-login';
  await page.getByLabel('Логин', {exact: true}).fill('dev_admin');
  stage = 'admin-login/fill-private-password';
  await page.getByLabel('Пароль', {exact: true}).fill(password);
  stage = 'admin-login/submit-and-wait-response';
  const [response] = await Promise.all([
    page.waitForResponse(value => new URL(value.url()).pathname === '/bff/auth/login' && value.request().method() === 'POST'),
    page.getByRole('button', {name: 'Войти', exact: true}).click()
  ]);
  assert.equal(response.status(), 200);
  stage = 'admin-login/wait-cabinet';
  await page.waitForURL('**/cabinet/admin/**');
  authenticated.add(page);
  stage = 'scan/open-actual-generated-route';
  await page.goto(base + '/cabinet/admin/scan');
  stage = 'scan/wait-heading';
  await page.getByRole('heading', {name: 'Сканирование посещаемости', exact: true}).waitFor();
  await controls(stage);
}

const endpoint = suffix => `/bff/attendance/divisions/${fixture.division_id}${suffix}`;
const responseFor = (suffix, parameters = {}) => page.waitForResponse(response => {
  const url = new URL(response.url());
  return url.pathname === endpoint(suffix) && response.request().method() === 'GET' && response.status() === 200
    && Object.entries(parameters).every(([key, value]) => url.searchParams.get(key) === value);
});
async function value(response) { return (await response.json()).data.value; }

async function refreshRange(from, to, day, phase) {
  stage = phase + '/fill-date-from';
  await page.getByLabel('С даты', {exact: true}).fill(from);
  stage = phase + '/fill-date-to';
  await page.getByLabel('По дату', {exact: true}).fill(to);
  stage = phase + '/refresh-and-wait-read-responses';
  await controls(stage);
  const [rosterResponse, daysResponse, journalResponse] = await Promise.all([
    responseFor('/students'), responseFor('/class-days', {date_from: from, date_to: to}),
    responseFor(`/class-days/${day.id}/attendance`),
    page.getByRole('button', {name: 'Обновить', exact: true}).click()
  ]);
  stage = phase + '/decode-scoped-source';
  const [roster, days, journal] = await Promise.all([value(rosterResponse), value(daysResponse), value(journalResponse)]);
  stage = phase + '/wait-loading-hidden';
  await page.getByText('Загрузка журнала…', {exact: true}).waitFor({state: 'hidden'});
  assert.equal(journal.organization_id, fixture.organization_id);
  assert.equal(journal.division_id, fixture.division_id);
  assert.equal(journal.unmarked_is_absent, false);
  assert.deepEqual(journal.days.map(value => value.id), [day.id]);
  assert(roster.some(student => student.account_id === fixture.account_id));
  assert(!roster.some(student => student.account_id === scope.math_student.account_id));
  return {roster, days, journal};
}

async function assertPicker(expectedDays, selectedDay, phase) {
  stage = phase + '/assert-picker';
  const picker = page.getByLabel(/^День посещения/);
  assert.equal(await picker.inputValue(), String(selectedDay.id));
  const options = await picker.locator('option').evaluateAll(elements => elements.map(element => ({id: Number(element.value), date: element.textContent.trim()})));
  assert.deepEqual(options, expectedDays.map(day => ({id: day.id, date: day.date})));
  await controls(stage);
}

async function assertTable(roster, journal, day, marked, phase) {
  stage = phase + '/assert-visible-roster';
  await page.waitForFunction(expected => {
    const actual = [...document.querySelectorAll('section table tbody th[scope="row"]')].map(element => element.textContent.trim()).sort();
    return JSON.stringify(actual) === JSON.stringify(expected);
  }, roster.map(student => student.full_name).sort());
  const table = page.locator('section table');
  stage = phase + '/assert-one-date-column';
  const headers = await table.locator('thead th').allTextContents();
  assert.equal(headers.length, 5);
  assert.deepEqual(headers.slice(0, 4).map(value => value.trim()), ['Ученик', 'Класс', 'Группа', 'Школа']);
  assert.equal(headers[4].match(/^\d{4}-\d{2}-\d{2}/)?.[0], day.date);
  const student = roster.find(value => value.account_id === fixture.account_id);
  const row = page.getByRole('rowheader', {name: student.full_name, exact: true}).locator('..');
  assert.equal(await row.locator('td').count(), 4);
  const entry = journal.entries.find(value => value.account_id === fixture.account_id);
  const cell = row.locator('td').last();
  assert.equal(await cell.locator('select').count(), 0, 'scan table marks are read-only display');
  if (marked) {
    assert.equal(entry.id, fixture.entry.id);
    assert.equal(entry.class_day_id, fixture.second_day.id);
    assert.equal(entry.zap_id, null);
    assert.deepEqual(entry.type, fixture.entry.type, 'actual recorded type snapshot must reach scan UI');
    assert.equal((await cell.locator('span').innerText()).trim(), entry.type.mark || entry.type.name);
    await cell.getByText(entry.type.name, {exact: true}).waitFor();
  } else {
    assert.equal(entry, undefined, 'the primary pupil must remain unmarked on Jan1');
    await cell.getByText('Не отмечен', {exact: true}).waitFor();
  }
  assert(!JSON.stringify(await table.locator('tbody th[scope="row"]').allTextContents()).includes('DEV Attendance scope math_student ' + scope.run));
}

async function exportVisible(tag, roster, journal, day, marked) {
  stage = tag + '/download-actual-xlsx';
  const [download] = await Promise.all([
    page.waitForEvent('download'), page.getByRole('button', {name: 'Экспорт XLSX', exact: true}).click()
  ]);
  assert.equal(download.suggestedFilename(), `Посещаемость_${fixture.organization_id}_${fixture.division_id}_${day.date}_${day.date}.xlsx`);
  const filename = `${prefix}-${tag}.xlsx`, saved = path.join(output, filename);
  await download.saveAs(saved);
  assert.equal(await download.failure(), null);
  artifacts.push(filename);
  stage = tag + '/read-actual-xlsx';
  const book = XLSX.readFile(saved);
  assert.deepEqual(book.SheetNames, ['Посещаемость']);
  const sheet = book.Sheets['Посещаемость'];
  const headers = ['Организация ID', 'Подразделение ID', 'Ученик', 'Класс', 'Группа', 'Школа', 'Краткое название школы', day.date];
  assert.deepEqual(XLSX.utils.sheet_to_json(sheet, {header: 1, defval: null})[0], headers);
  const rows = XLSX.utils.sheet_to_json(sheet, {defval: null});
  assert.deepEqual(rows.map(row => row['Ученик']).sort(), roster.map(student => student.full_name).sort());
  for (const student of roster) {
    const row = rows.find(row => row['Ученик'] === student.full_name);
    assert.deepEqual(Object.keys(row), headers);
    assert.equal(row['Организация ID'], fixture.organization_id);
    assert.equal(row['Подразделение ID'], fixture.division_id);
    assert.equal(row['Класс'], student.class ?? '—');
    assert.equal(row['Группа'], student.group_name || (student.group_id == null ? '—' : `Группа #${student.group_id}`));
    assert.equal(row['Школа'], student.school_name || student.school_short_name || (student.school_id == null ? '—' : `Школа #${student.school_id}`));
    assert.equal(row['Краткое название школы'], student.school_short_name || '—');
    const entry = journal.entries.find(value => value.class_day_id === day.id && value.account_id === student.account_id);
    assert.equal(row[day.date], entry?.type.name ?? 'Не отмечен');
  }
  const primary = rows.find(row => row['Ученик'] === fixture.entry.full_name);
  assert.equal(primary[day.date], marked ? fixture.entry.type.name : 'Не отмечен');
  assert(!JSON.stringify(rows).includes('DEV Attendance scope math_student ' + scope.run));
  await screenshot(tag);
}

async function run() {
  scope = JSON.parse(await fs.readFile(path.join(output, 'attendance-scope-fixtures.json'), 'utf8'));
  fixture = JSON.parse(await fs.readFile(path.join(output, 'attendance-scan-fixtures.json'), 'utf8'));
  assert.equal(fixture.run, scope.run);
  assert.equal(fixture.run, '0a242b5545');
  assert.equal(fixture.organization_id, 1001);
  assert.equal(fixture.division_id, scope.d1);
  assert.equal(fixture.account_id, scope.student.account_id);
  assert.equal(fixture.first_day.date, '2036-01-01');
  assert.equal(fixture.second_day.date, '2036-01-02');
  assert.notEqual(fixture.first_day.id, fixture.second_day.id);
  assert.equal(fixture.entry.class_day_id, fixture.second_day.id);
  assert.equal(fixture.entry.account_id, fixture.account_id);
  assert.equal(fixture.entry.zap_id, null);
  assert.equal(fixture.entry.is_deleted, false);
  assert.equal(fixture.type.is_active, true);
  assert.equal(fixture.type.code, 'scoped_excuse');
  assert.deepEqual(fixture.entry.type, fixture.type);
  ({password} = JSON.parse(await fs.readFile(path.join(output, 'accounts.json'), 'utf8')));
  XLSX = createRequire(path.join(root, 'cpm_front_new/package.json'))('xlsx');
  const {chromium} = await import('/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs');
  stage = 'launch-local-chromium';
  browser = await chromium.launch({executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true, args: ['--no-proxy-server']});
  await login();
  stage = 'scan/read-auth-context';
  const initialContext = await page.getByLabel('Переключить кабинет и подразделение', {exact: true}).inputValue();
  stage = 'scan/select-own-fixture-division';
  await controls(stage);
  await page.getByLabel(/^Подразделение журнала/).selectOption(String(fixture.division_id));

  const first = await refreshRange(fixture.first_day.date, fixture.second_day.date, fixture.first_day, 'first-day');
  assert.deepEqual(first.days.map(day => ({id: day.id, date: day.date})), [fixture.first_day, fixture.second_day].map(day => ({id: day.id, date: day.date})));
  await assertPicker([fixture.first_day, fixture.second_day], fixture.first_day, 'first-day');
  await assertTable(first.roster, first.journal, fixture.first_day, false, 'first-day');
  await exportVisible('first-day', first.roster, first.journal, fixture.first_day, false);
  checks.push('Full period picker contains Jan1 and Jan2; initial table/XLSX contains Jan1 only and primary pupil is unmarked');

  stage = 'second-day/select-and-wait-journal';
  const secondURL = base + endpoint(`/class-days/${fixture.second_day.id}/attendance`);
  let releaseRead;
  const heldRead = new Promise(resolve => { releaseRead = resolve; });
  // Hold only the outgoing real GET. Its source, data and eventual response are
  // unchanged; this exposes the loading state before the next day can arrive.
  const holdSecondRead = async route => {
    if (route.request().method() !== 'GET') {
      blockedWrites.push({method: route.request().method(), path: new URL(route.request().url()).pathname});
      return route.abort();
    }
    await heldRead;
    return route.continue();
  };
  await page.route(secondURL, holdSecondRead);
  const secondResponsePromise = responseFor(`/class-days/${fixture.second_day.id}/attendance`);
  // Attach a rejection handler immediately if an assertion fails before await.
  secondResponsePromise.catch(() => {});
  let secondResponse;
  try {
    await Promise.all([
      page.waitForRequest(request => request.url() === secondURL && request.method() === 'GET'),
      page.getByLabel(/^День посещения/).selectOption(String(fixture.second_day.id))
    ]);
    stage = 'second-day/assert-pending-read-controls';
    assert.equal(await page.getByLabel(/^День посещения/).isDisabled(), true, 'loaded day picker cannot drop a second selection while loading');
    assert.equal(await page.getByRole('button', {name: 'Экспорт XLSX', exact: true}).isDisabled(), true);
    assert.equal(await page.getByRole('button', {name: 'Сканировать камерой', exact: true}).isDisabled(), true);
    releaseRead();
    secondResponse = await secondResponsePromise;
  } finally {
    releaseRead();
    await page.unroute(secondURL, holdSecondRead);
  }
  const second = await value(secondResponse);
  stage = 'second-day/wait-loading-hidden';
  await page.getByText('Загрузка журнала…', {exact: true}).waitFor({state: 'hidden'});
  assert.equal(second.organization_id, fixture.organization_id);
  assert.equal(second.division_id, fixture.division_id);
  assert.equal(second.unmarked_is_absent, false);
  assert.deepEqual(second.days.map(day => day.id), [fixture.second_day.id]);
  await assertPicker([fixture.first_day, fixture.second_day], fixture.second_day, 'second-day');
  await assertTable(first.roster, second, fixture.second_day, true, 'second-day');
  await exportVisible('second-day', first.roster, second, fixture.second_day, true);
  checks.push('Day picker/export/camera wait for the real Jan2 GET; selecting Jan2 shows its actual manual snapshot and exports Jan2 only');

  const fallback = await refreshRange(fixture.first_day.date, fixture.first_day.date, fixture.first_day, 'fallback');
  assert.deepEqual(fallback.days.map(day => day.id), [fixture.first_day.id]);
  await assertPicker([fixture.first_day], fixture.first_day, 'fallback');
  await assertTable(fallback.roster, fallback.journal, fixture.first_day, false, 'fallback');
  await exportVisible('fallback', fallback.roster, fallback.journal, fixture.first_day, false);
  assert.equal(await page.getByLabel('Переключить кабинет и подразделение', {exact: true}).inputValue(), initialContext);
  checks.push('Excluding selected Jan2 resolves picker/table/XLSX to valid Jan1 without changing the auth cabinet');
  assert.deepEqual(errors, []);
  assert.deepEqual(external, []);
  assert.deepEqual(blockedWrites, []);
  checks.push('No JavaScript errors, external requests, QR/camera actions or attempted domain writes');
}

try { await run(); }
catch (error) {
  failure = {stage, type: safeErrorName(error)};
  try { await controls(stage + '/failure'); } catch { /* No raw browser errors. */ }
  try { await screenshot('failure'); } catch { /* No raw screenshot errors. */ }
} finally {
  stage = 'close-own-auth-session';
  if (page && authenticated.has(page) && new URL(page.url()).pathname.startsWith('/cabinet/')) {
    try {
      await Promise.all([
        page.waitForURL('**/login'), page.getByRole('button', {name: 'Выйти из кабинета', exact: true}).click()
      ]);
      authenticated.delete(page);
    } catch (error) { failure ??= {stage, type: safeErrorName(error)}; }
  }
  if (context) { try { await context.close(); } catch { failure ??= {stage, type: 'Error'}; } }
  if (browser) { try { await browser.close(); } catch { failure ??= {stage, type: 'Error'}; } }
  if (errors.length || external.length || blockedWrites.length) failure ??= {stage: 'final-browser-safety-check', type: 'AssertionError'};
  const report = {status: failure ? 'failed' : 'passed', checked_at: new Date().toISOString(), checks,
    fixture_run: fixture?.run, organization_id: fixture?.organization_id, division_id: fixture?.division_id,
    account_id: fixture?.account_id, first_day_id: fixture?.first_day?.id, second_day_id: fixture?.second_day?.id,
    entry_id: fixture?.entry?.id, recorded_type_id: fixture?.entry?.type?.id, route: '/cabinet/admin/scan',
    artifacts, control_diagnostics: diagnostics, javascript_errors: errors, external_requests: external, blocked_domain_writes: blockedWrites,
    scope: 'existing own synthetic scan fixtures; fresh isolated Auth session only; domain read-only; camera and QR not invoked',
    dependencies: ['optimized UI3001 with selected scan day fix', 'BFF8001', 'Attendance8013', 'existing Core metadata projection', 'actual XLSX browser downloads']};
  if (failure) report.failure = failure;
  try { await fs.writeFile(path.join(output, `${prefix}-report.json`), JSON.stringify(report, null, 2) + '\n'); reportSaved = true; }
  catch (error) { failure ??= {stage: 'write-token-free-report', type: safeErrorName(error)}; }
}
if (failure) {
  console.error(`Scan browser acceptance failed at ${failure.stage} (${failure.type}); token-free report ${reportSaved ? 'saved' : 'unavailable'}.`);
  process.exitCode = 1;
} else console.log(`Passed ${checks.length} read-only scan day-picker/table/XLSX scenarios; token-free report saved.`);
