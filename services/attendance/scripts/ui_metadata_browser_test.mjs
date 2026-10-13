// Read-only journal metadata/filter/XLSX acceptance after BFF and UI owners say READY.
// No browser or HTTP work occurs without --run. Errors never expose Playwright
// messages, credentials, cookies, storage state, traces or HAR files.
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createRequire} from 'node:module';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const output = path.join(root, 'dev/local');
const base = 'http://127.0.0.1:3001';
const date = '2036-01-01';
const prefix = 'attendance-ui-metadata';
const args = process.argv.slice(2);
if (args.includes('--help')) {
  console.log('After BFF8001 and optimized UI3001 READY: node services/attendance/scripts/ui_metadata_browser_test.mjs --run');
  console.log('Uses existing private Attendance scope/projection fixtures; only Auth sessions change. Domain writes are blocked.');
  process.exit(0);
}
if (args.length !== 1 || args[0] !== '--run') {
  console.error('Use --help, or --run only after the BFF/UI owner execution window is agreed.');
  process.exit(2);
}

const checks = [], javascriptErrors = [], external = [], blockedWrites = [], artifacts = [], sessions = [], diagnostics = [];
let browser, activePage, stage = 'read-private-dev-fixtures', failure, reportSaved = false;
const safeErrorName = error => ['Error', 'AssertionError', 'TimeoutError', 'TypeError', 'RangeError', 'SyntaxError'].includes(error?.name) ? error.name : 'Error';
const check = label => checks.push(label);
const authenticated = new Set();
let fixtures, projection, password, XLSX;

async function screenshot(page, tag) {
  if (!authenticated.has(page) || !new URL(page.url()).pathname.startsWith('/cabinet/')) return;
  const filename = `${prefix}-${tag}.png`;
  await page.screenshot({path: path.join(output, filename), fullPage: true});
  artifacts.push(filename);
}

// Only known authenticated controls: IDs, selected IDs, disabled state and dates.
// Never collect option text, general DOM, login fields, request bodies or identity DTOs.
async function selectDiagnostics(page, tag, expectedDivision) {
  if (!authenticated.has(page) || !new URL(page.url()).pathname.startsWith('/cabinet/')) return;
  const controls = await page.evaluate(() => {
    const labelText = element => [...(element.closest('label')?.childNodes ?? [])]
      .filter(node => node.nodeType === Node.TEXT_NODE).map(node => node.textContent).join('').trim();
    const safeID = value => /^(?:all|none|\d+(?::\d*)?)$/.test(value) ? value : '<non-id>';
    const selects = [...document.querySelectorAll('select')];
    const result = {};
    for (const [key, label] of [['auth_context', 'Переключить кабинет и подразделение'], ['journal_division', 'Подразделение журнала'], ['class', 'Класс'], ['group', 'Группа'], ['school', 'Школа']]) {
      const matches = selects.filter(element => element.getAttribute('aria-label') === label || labelText(element) === label);
      result[key] = {matches: matches.length, controls: matches.map(element => ({
        selected_id: safeID(element.value), disabled: element.disabled,
        option_ids: [...element.options].map(option => safeID(option.value))
      }))};
    }
    for (const [key, label] of [['date_from', 'С даты'], ['date_to', 'По дату']]) {
      const matches = [...document.querySelectorAll('input[type="date"]')].filter(element => labelText(element) === label);
      result[key] = {matches: matches.length, values: matches.map(element => /^\d{4}-\d{2}-\d{2}$/.test(element.value) ? element.value : '<non-date>')};
    }
    return result;
  });
  diagnostics.push({stage: tag, expected_division_id: expectedDivision, expected_date: date, controls});
}

async function login(loginName, role) {
  const phase = `${role}-login`;
  stage = phase + '/create-isolated-session';
  const context = await browser.newContext({viewport: {width: 1440, height: 1000}, acceptDownloads: true});
  const page = await context.newPage();
  sessions.push({context, page});
  activePage = page;
  page.setDefaultTimeout(25000);
  page.on('pageerror', error => javascriptErrors.push({stage, type: safeErrorName(error)}));
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
  stage = phase + '/open-login';
  await page.goto(base + '/login');
  stage = phase + '/fill-private-login';
  await page.getByLabel('Логин', {exact: true}).fill(loginName);
  stage = phase + '/fill-private-password';
  await page.getByLabel('Пароль', {exact: true}).fill(password);
  stage = phase + '/submit-and-wait-auth-response';
  const [result] = await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/login' && response.request().method() === 'POST'),
    page.getByRole('button', {name: 'Войти', exact: true}).click()
  ]);
  assert.equal(result.status(), 200, 'fresh private BFF session login must succeed');
  stage = phase + '/wait-authenticated-cabinet';
  await page.waitForURL(`**/cabinet/${role}/**`);
  authenticated.add(page);
  stage = phase + '/open-journal';
  await page.goto(`${base}/cabinet/${role}/attendance`);
  stage = phase + '/wait-journal-heading';
  await page.getByRole('heading', {name: 'Посещаемость', exact: true}).first().waitFor();
  await selectDiagnostics(page, stage);
  return page;
}

function filters(page) {
  return ['Класс', 'Группа', 'Школа'].map(name => page.getByLabel(new RegExp('^' + name)));
}

async function visibleNames(page, names) {
  const expected = [...names].sort();
  await page.waitForFunction(expectedNames => {
    const actual = [...document.querySelectorAll('section table tbody th[scope="row"]')]
      .map(element => element.textContent.trim()).sort();
    return JSON.stringify(actual) === JSON.stringify(expectedNames);
  }, expected);
}

async function loadJournal(page, division, phase) {
  activePage = page;
  stage = phase + '/select-journal-division';
  await selectDiagnostics(page, stage, division);
  await page.getByLabel(/^Подразделение журнала/).selectOption(String(division));
  stage = phase + '/fill-date-from';
  await selectDiagnostics(page, stage, division);
  await page.getByLabel('С даты', {exact: true}).fill(date);
  stage = phase + '/fill-date-to';
  await page.getByLabel('По дату', {exact: true}).fill(date);
  stage = phase + '/refresh-and-wait-roster-report';
  await selectDiagnostics(page, stage, division);
  const prefix = `/bff/attendance/divisions/${division}`;
  const [rosterResult, reportResult] = await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === prefix + '/students' && response.request().method() === 'GET' && response.status() === 200),
    page.waitForResponse(response => {
      const url = new URL(response.url());
      return url.pathname === prefix + '/attendance-report' && url.searchParams.get('date_from') === date && url.searchParams.get('date_to') === date && response.request().method() === 'GET' && response.status() === 200;
    }),
    page.getByRole('button', {name: 'Обновить', exact: true}).click()
  ]);
  stage = phase + '/decode-roster-report';
  const [rosterEnvelope, reportEnvelope] = await Promise.all([rosterResult.json(), reportResult.json()]);
  stage = phase + '/wait-journal-loading-hidden';
  await page.getByText('Загрузка журнала…', {exact: true}).waitFor({state: 'hidden'});
  stage = phase + '/validate-scoped-source';
  await selectDiagnostics(page, stage, division);
  const roster = rosterEnvelope.data.value, journal = reportEnvelope.data.value;
  assert.equal(journal.organization_id, 1001);
  assert.equal(journal.division_id, division);
  assert.equal(journal.unmarked_is_absent, false);
  assert(journal.days.some(day => day.date === date), 'existing synthetic class day is required');
  assert(roster.length > 0, 'real roster is required, not an empty placeholder');
  assert(!roster.some(student => student.account_id === fixtures.math_student.account_id), 'foreign organization pupil must not enter this roster');
  return {roster, journal};
}

function ownStudent(roster) {
  const student = roster.find(row => row.account_id === fixtures.student.account_id);
  assert(student, 'existing own synthetic pupil must be present');
  assert.equal(student.full_name, 'DEV Attendance scope student ' + fixtures.run);
  assert.equal(student.class, 10);
  assert.equal(student.school_id, projection.schools['1001'].id);
  assert.equal(student.school_name, projection.schools['1001'].name);
  assert.equal(student.school_short_name, projection.schools['1001'].short_name);
  return student;
}

async function assertMetadataRow(page, student, expectedGroup) {
  const row = page.getByRole('rowheader', {name: student.full_name, exact: true}).locator('..');
  await row.waitFor();
  const cells = row.locator('td');
  assert.equal((await cells.nth(0).innerText()).trim(), '10');
  assert.equal((await cells.nth(1).innerText()).trim(), expectedGroup);
  assert.equal((await cells.nth(2).innerText()).trim(), projection.schools['1001'].short_name);
  assert.equal(await cells.nth(2).getAttribute('title'), projection.schools['1001'].name);
  return row;
}

async function exportVisible(page, tag, division, expectedStudents, journal) {
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', {name: 'Экспорт XLSX', exact: true}).click()
  ]);
  assert.equal(download.suggestedFilename(), `Посещаемость_1001_${division}_${date}_${date}.xlsx`);
  const filename = `${prefix}-${tag}.xlsx`, saved = path.join(output, filename);
  await download.saveAs(saved);
  assert.equal(await download.failure(), null, 'actual XLSX download must complete');
  artifacts.push(filename);
  const book = XLSX.readFile(saved);
  assert.deepEqual(book.SheetNames, ['Посещаемость']);
  const sheet = book.Sheets['Посещаемость'];
  const matrix = XLSX.utils.sheet_to_json(sheet, {header: 1, defval: null});
  assert.deepEqual(matrix[0], ['Организация ID', 'Подразделение ID', 'Ученик', 'Класс', 'Группа', 'Школа', 'Краткое название школы', date]);
  const rows = XLSX.utils.sheet_to_json(sheet, {defval: null});
  assert.deepEqual(rows.map(row => row['Ученик']).sort(), expectedStudents.map(student => student.full_name).sort());
  for (const student of expectedStudents) {
    const row = rows.find(value => value['Ученик'] === student.full_name);
    assert.equal(row['Организация ID'], 1001);
    assert.equal(row['Подразделение ID'], division);
    assert.equal(row['Класс'], student.class ?? '—');
    assert.equal(row['Группа'], student.group_name || (student.group_id == null ? '—' : `Группа #${student.group_id}`));
    assert.equal(row['Школа'], student.school_name || student.school_short_name || (student.school_id == null ? '—' : `Школа #${student.school_id}`));
    assert.equal(row['Краткое название школы'], student.school_short_name || '—');
    const day = journal.days.find(value => value.date === date);
    const entry = journal.entries.find(value => value.class_day_id === day.id && value.account_id === student.account_id);
    assert.equal(row[date], entry?.type.name ?? 'Не отмечен');
  }
  assert(!JSON.stringify(rows).includes('DEV Attendance scope math_student ' + fixtures.run));
  assert(!JSON.stringify(rows).includes(projection.schools['2001'].short_name));
  return rows;
}

async function run() {
  fixtures = JSON.parse(await fs.readFile(path.join(output, 'attendance-scope-fixtures.json'), 'utf8'));
  projection = JSON.parse(await fs.readFile(path.join(output, 'attendance-core-projection-fixtures.json'), 'utf8'));
  assert.equal(fixtures.run, projection.run, 'scope and projection fixtures must be from the same own run');
  assert(fixtures.student.login.startsWith('dev_att_scope_' + fixtures.run + '_'));
  assert.deepEqual(fixtures.organizations, [1001, 2001]);
  ({password} = JSON.parse(await fs.readFile(path.join(output, 'accounts.json'), 'utf8')));
  const require = createRequire(path.join(root, 'cpm_front_new/package.json'));
  XLSX = require('xlsx');
  const {chromium} = await import('/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs');
  stage = 'launch-local-chromium';
  browser = await chromium.launch({executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true, args: ['--no-proxy-server']});

  stage = 'admin-login-and-d1-load';
  const admin = await login('dev_admin', 'admin');
  stage = 'admin-d1/read-current-auth-context';
  await selectDiagnostics(admin, stage, fixtures.d1);
  const initialContext = await admin.getByLabel('Переключить кабинет и подразделение', {exact: true}).inputValue();
  const first = await loadJournal(admin, fixtures.d1, 'admin-d1');
  stage = 'admin-d1/validate-metadata-and-visible-roster';
  const student = ownStudent(first.roster);
  assert.equal(student.group_id, fixtures.group);
  assert.equal(student.group_name, 'DEV Attendance scope group ' + fixtures.run);
  await visibleNames(admin, first.roster.map(value => value.full_name));
  await assertMetadataRow(admin, student, student.group_name);
  assert(!first.journal.entries.some(entry => entry.account_id === student.account_id), 'existing D1 fixture must remain unmarked after own zap unlink');
  assert.equal(await admin.getByRole('combobox', {name: `Отметка ${student.full_name} ${date}`, exact: true}).inputValue(), '');
  check('Admin D1 shows actual class, group and school metadata; unmarked is not absence');

  stage = 'admin-positive-filters-and-xlsx';
  const [classFilter, groupFilter, schoolFilter] = filters(admin);
  await classFilter.selectOption('10');
  await groupFilter.selectOption(String(fixtures.group));
  await schoolFilter.selectOption(String(projection.schools['1001'].id));
  await visibleNames(admin, [student.full_name]);
  const filtered = await exportVisible(admin, 'admin-filtered', fixtures.d1, [student], first.journal);
  assert.equal(filtered[0][date], 'Не отмечен');
  await screenshot(admin, 'admin-filtered');
  check('Class/group/school filters retain own pupil; actual XLSX contains only visible metadata and unmarked day');

  stage = 'admin-null-and-empty-filters';
  await schoolFilter.selectOption('none');
  await visibleNames(admin, []);
  await admin.getByText('По выбранным фильтрам учеников нет.', {exact: true}).waitFor();
  await exportVisible(admin, 'admin-empty', fixtures.d1, [], first.journal);
  await screenshot(admin, 'admin-empty');
  check('Incompatible filters show an empty result and export headers without invented pupil rows');
  await groupFilter.selectOption('none');
  const nullStudents = first.roster.filter(value => value.class === 10 && value.group_id == null && value.school_id == null);
  assert(nullStudents.some(value => value.account_id === fixtures.other_student.account_id), 'existing nullable pupil fixture is required');
  await visibleNames(admin, nullStudents.map(value => value.full_name));
  for (const value of nullStudents) {
    const row = admin.getByRole('rowheader', {name: value.full_name, exact: true}).locator('..');
    assert.equal((await row.locator('td').nth(1).innerText()).trim(), '—');
    assert.equal((await row.locator('td').nth(2).innerText()).trim(), '—');
  }
  await exportVisible(admin, 'admin-null', fixtures.d1, nullStudents, first.journal);
  check('Unknown group/school remain explicit null display and are filterable/exported without invented metadata');

  stage = 'admin-reset-and-d2-scope';
  await admin.getByRole('button', {name: 'Сбросить фильтры', exact: true}).click();
  for (const filter of filters(admin)) assert.equal(await filter.inputValue(), 'all');
  await visibleNames(admin, first.roster.map(value => value.full_name));
  await groupFilter.selectOption(String(fixtures.group));
  await schoolFilter.selectOption(String(projection.schools['1001'].id));
  const second = await loadJournal(admin, fixtures.d2, 'admin-d2');
  stage = 'admin-d2/validate-filter-reset-and-metadata';
  for (const filter of filters(admin)) assert.equal(await filter.inputValue(), 'all');
  const secondStudent = ownStudent(second.roster);
  assert.equal(secondStudent.group_id, null, 'own membership group must be scoped independently in D2');
  assert.equal(secondStudent.group_name, null);
  await visibleNames(admin, second.roster.map(value => value.full_name));
  await assertMetadataRow(admin, secondStudent, '—');
  stage = 'admin-d2/read-current-auth-context';
  await selectDiagnostics(admin, stage, fixtures.d2);
  assert.equal(await admin.getByLabel('Переключить кабинет и подразделение', {exact: true}).inputValue(), initialContext);
  await screenshot(admin, 'admin-d2');
  check('Reset restores roster; journal division switch resets filters and retains organization profile with D2-only membership group');

  stage = 'student-read-only-d1-and-xlsx';
  const pupil = await login(fixtures.student.login, 'student');
  const own = await loadJournal(pupil, fixtures.d1, 'student-d1'), ownRow = ownStudent(own.roster);
  stage = 'student-d1/validate-metadata-and-visible-roster';
  assert.deepEqual(own.roster.map(value => value.account_id), [fixtures.student.account_id]);
  await visibleNames(pupil, [ownRow.full_name]);
  await assertMetadataRow(pupil, ownRow, student.group_name);
  assert.equal(await pupil.getByRole('button', {name: 'Создать день занятий', exact: true}).count(), 0);
  assert.equal(await pupil.getByRole('button', {name: 'Добавить тип посещения', exact: true}).count(), 0);
  const ownDisplay = pupil.getByRole('rowheader', {name: ownRow.full_name, exact: true}).locator('..');
  await ownDisplay.getByText('Не отмечен', {exact: true}).waitFor();
  await pupil.getByLabel(/^Класс/).selectOption('10');
  await pupil.getByLabel(/^Группа/).selectOption(String(fixtures.group));
  await pupil.getByLabel(/^Школа/).selectOption(String(projection.schools['1001'].id));
  await visibleNames(pupil, [ownRow.full_name]);
  await exportVisible(pupil, 'student-filtered', fixtures.d1, [ownRow], own.journal);
  await screenshot(pupil, 'student-d1');
  check('Student journal and downloaded XLSX show only self with actual metadata and no write controls');

  stage = 'student-d2-filter-reset';
  const ownSecond = await loadJournal(pupil, fixtures.d2, 'student-d2'), ownSecondRow = ownStudent(ownSecond.roster);
  stage = 'student-d2/validate-filter-reset-and-metadata';
  for (const filter of filters(pupil)) assert.equal(await filter.inputValue(), 'all');
  assert.deepEqual(ownSecond.roster.map(value => value.account_id), [fixtures.student.account_id]);
  assert.equal(ownSecondRow.group_id, null);
  await visibleNames(pupil, [ownSecondRow.full_name]);
  await assertMetadataRow(pupil, ownSecondRow, '—');
  await exportVisible(pupil, 'student-d2', fixtures.d2, [ownSecondRow], ownSecond.journal);
  await screenshot(pupil, 'student-d2');
  check('Student D2 resets D1 filters and exports only the selected division with its own membership metadata');
  assert.deepEqual(javascriptErrors, []);
  assert.deepEqual(external, []);
  assert.deepEqual(blockedWrites, []);
  check('No JavaScript errors, external requests or attempted domain writes');
}

try {
  await run();
} catch (error) {
  failure = {stage, type: safeErrorName(error)};
  if (activePage) {
    try { await selectDiagnostics(activePage, stage + '/failure'); } catch { /* No raw browser errors. */ }
    try { await screenshot(activePage, 'failure'); } catch { /* Do not print screenshot errors. */ }
  }
} finally {
  stage = 'close-own-auth-sessions';
  for (const {page, context} of sessions) {
    try {
      if (authenticated.has(page) && new URL(page.url()).pathname.startsWith('/cabinet/')) {
        await page.getByRole('button', {name: 'Выйти из кабинета', exact: true}).click();
        await page.waitForURL('**/login');
        authenticated.delete(page);
      }
    } catch (error) {
      failure ??= {stage, type: safeErrorName(error)};
    }
    try { await context.close(); } catch { failure ??= {stage, type: 'Error'}; }
  }
  if (browser) {
    try { await browser.close(); } catch { failure ??= {stage, type: 'Error'}; }
  }
  if (javascriptErrors.length || external.length || blockedWrites.length) failure ??= {stage: 'final-browser-safety-check', type: 'AssertionError'};
  const report = {status: failure ? 'failed' : 'passed', checked_at: new Date().toISOString(), checks,
    fixture_run: fixtures?.run, organizations: [1001, 2001], divisions: fixtures ? [fixtures.d1, fixtures.d2, fixtures.math_division] : [],
    artifacts, control_diagnostics: diagnostics, javascript_errors: javascriptErrors, external_requests: external, blocked_domain_writes: blockedWrites,
    scope: 'existing synthetic fixtures; only fresh Auth login/logout sessions; domain read-only',
    dependencies: ['actual optimized UI3001', 'actual BFF8001', 'actual Attendance8013', 'actual Core scoped metadata projection', 'actual XLSX browser download']};
  if (failure) report.failure = failure;
  try { await fs.writeFile(path.join(output, `${prefix}-report.json`), JSON.stringify(report, null, 2) + '\n'); reportSaved = true; }
  catch (error) { failure ??= {stage: 'write-token-free-report', type: safeErrorName(error)}; }
}
if (failure) {
  console.error(`Metadata browser acceptance failed at ${failure.stage} (${failure.type}); token-free report ${reportSaved ? 'saved' : 'unavailable'}.`);
  process.exitCode = 1;
} else console.log(`Passed ${checks.length} read-only journal metadata/filter/XLSX browser scenarios; token-free report saved.`);
