// Repeatable read-only smoke of the optimized local UI using the API demo.
// Run after the full lifecycle browser checks and the optimized UI is ready.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const output = root + 'dev/local/';
const base = 'http://127.0.0.1:3001';
const demo = JSON.parse(await fs.readFile(output + 'bff-exams-demo.json', 'utf8'));
const { password } = JSON.parse(await fs.readFile(output + 'accounts.json', 'utf8'));
assert(demo.classic_exam_id && demo.outside_exam_id && demo.retake_attempt_id && demo.examiners?.[0]?.login, 'Final API demo is required');
const checks = [], errors = [], external = [], forbiddenWrites = [];
let activePage;
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const pass = name => { checks.push(name); console.log(JSON.stringify({ check: name })); };

async function read(context, path) {
  assert(path.startsWith('/exams') || path.startsWith('/core/'), 'Only public read APIs are permitted');
  const me = await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
  assert.equal(me.status(), 200);
  const identity = await me.json();
  const response = await context.request.get(base + '/bff' + path, {
    headers: { 'X-CPM-Request': '1', 'X-CPM-Context': identity.context_revision },
  });
  assert.equal(response.status(), 200, `GET ${path} returned ${response.status()}`);
  return (await response.json()).data;
}

async function login(login, role) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await context.addInitScript(() => localStorage.setItem('flash_onboarding_seen', 'true'));
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) {
      external.push(url.origin + url.pathname);
      return route.abort();
    }
    if (url.pathname.startsWith('/bff/exams') && request.method() !== 'GET') {
      forbiddenWrites.push(request.method() + ' ' + url.pathname);
      return route.abort();
    }
    return route.continue();
  });
  const page = await context.newPage();
  activePage = page;
  page.setDefaultTimeout(25000);
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(base + '/login');
  await page.getByLabel('Логин', { exact: true }).fill(login);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await page.waitForURL('**/cabinet/**');
  const selector = page.getByLabel('Переключить кабинет и подразделение');
  await selector.waitFor();
  const value = `${demo.organization_id}:${demo.division_a}`;
  if (await selector.inputValue() !== value) {
    const switched = page.waitForResponse(response => new URL(response.url()).pathname === '/bff/auth/context' && response.request().method() === 'POST');
    await selector.selectOption(value);
    assert.equal((await switched).status(), 200);
    await page.waitForFunction(value => document.querySelector('[aria-label="Переключить кабинет и подразделение"]')?.value === value, value);
  }
  const cookie = (await context.cookies(base + '/bff/auth/me')).find(item => item.name === 'cpm_bff');
  assert(cookie?.httpOnly && cookie.sameSite === 'Strict', 'The session cookie must be HttpOnly and SameSite=Strict');
  assert.equal(await page.evaluate(() => document.cookie.includes('cpm_bff=')), false);
  return { context, page, role };
}

async function mobile(page, image) {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.waitForTimeout(350); // The cabinet sidebar changes layout on viewport resize.
  const geometry = await page.evaluate(() => ({ width: document.documentElement.scrollWidth, viewport: innerWidth,
    offenders: [...document.querySelectorAll('*')].filter(element => element.getBoundingClientRect().right > innerWidth + 1).slice(0, 8).map(element => ({ tag: element.tagName, class: element.className, right: element.getBoundingClientRect().right })) }));
  await page.screenshot({ path: output + image, fullPage: true });
  assert(geometry.width <= geometry.viewport + 1, 'Mobile page overflows: ' + JSON.stringify(geometry));
}

let status = 'failed', failure;
try {
  let session = await login('dev_admin', 'admin');
  let { context, page } = session;
  const classic = await read(context, `/exams/${demo.classic_exam_id}`);
  const outside = await read(context, `/exams/${demo.outside_exam_id}`);
  const privateExam = await read(context, `/exams/${demo.private_division_b_exam_id}`);
  await page.goto(base + '/cabinet/admin/exams?scope=organization&search=' + encodeURIComponent(privateExam.name));
  await page.getByRole('heading', { level: 2, name: privateExam.name, exact: true }).waitFor();
  await page.getByRole('button', { name: 'Все экзамены организации', exact: true }).waitFor();
  pass('optimized organization admin catalog includes a private exam from another division');
  await page.goto(base + '/cabinet/admin/exams?scope=bank');
  await page.getByRole('button', { name: 'Банк организации', exact: true }).waitFor();
  await page.locator('[aria-label="Каталог экзаменов"] button[aria-current="page"]').filter({ hasText: 'Банк организации' }).waitFor();
  const bank = await read(context, '/exams?scope=bank&limit=200');
  if (bank.items.length) await page.getByRole('heading', { level: 2 }).first().waitFor();
  else await page.getByText('Экзаменов пока нет.', { exact: true }).waitFor();
  assert(bank.items.every(item => item.in_bank === true));
  pass('optimized organization bank uses the owner bank scope');
  await mobile(page, 'bff-exams-optimized-admin-mobile.png');
  pass('optimized admin catalog fits a 390px viewport');
  await context.close();

  ({ context, page } = await login(demo.scoped_staff_login, 'staff_admin'));
  await page.goto(base + `/cabinet/staff_admin/exams?examId=${demo.classic_exam_id}`);
  await page.getByRole('heading', { level: 1, name: classic.name, exact: true }).waitFor();
  await page.getByRole('navigation', { name: 'Разделы экзамена', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Все экзамены организации', exact: true }).count(), 0);
  pass('optimized scoped editor reads its selected division exam with no organization catalog');
  await context.close();

  ({ context, page } = await login(demo.view_staff_login, 'staff_admin'));
  await page.goto(base + `/cabinet/staff_admin/exams?examId=${demo.classic_exam_id}&tab=results`);
  await page.getByRole('heading', { name: 'Текущие результаты', exact: true }).waitFor();
  await page.getByText('0 / 5', { exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Создать экзамен', exact: true }).count(), 0);
  assert.equal(await page.getByRole('button', { name: 'Удалить экзамен', exact: true }).count(), 0);
  assert.equal(await page.getByRole('button', { name: 'Назначить пересдачу', exact: true }).count(), 0);
  pass('optimized view-only staff reads zero results without mutation actions');
  await context.close();

  ({ context, page } = await login(demo.student_login, 'student'));
  await page.goto(base + `/cabinet/student/exams?examId=${demo.classic_exam_id}`);
  await page.getByRole('heading', { level: 1, name: classic.name, exact: true }).waitFor();
  await page.getByRole('button', { name: /Текущий результат.*0 \/ 5/ }).waitFor();
  await page.getByRole('heading', { name: 'Пересдача', exact: true }).waitFor();
  await page.getByRole('heading', { name: 'Протокол вопросов', exact: true }).waitFor();
  pass('optimized student detail shows the real exam name and current worse zero retake');
  await page.getByRole('button', { name: /Предыдущая сдача.*4 \/ 5/ }).click();
  await page.getByRole('heading', { name: 'Первая сдача', exact: true }).waitFor();
  await page.getByText('После апелляции', { exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'История голосования', exact: true }).count(), 0);
  pass('optimized student history keeps the appealed first result and hides examiner voting history');
  await page.goto(base + `/cabinet/student/exams?examId=${demo.outside_exam_id}`);
  await page.getByRole('heading', { level: 1, name: outside.name, exact: true }).waitFor();
  await page.getByText('Баллы: 123,45', { exact: true }).waitFor();
  await page.getByText('2 / 5', { exact: true }).waitFor();
  assert.equal(await page.getByRole('heading', { name: 'Протокол вопросов', exact: true }).count(), 0);
  pass('optimized outside result keeps decimal score separate from grade');
  await mobile(page, 'bff-exams-optimized-student-mobile.png');
  pass('optimized student result fits a 390px viewport');
  await context.close();

  ({ context, page } = await login(demo.examiners[0].login, 'examinator'));
  await page.goto(base + `/cabinet/examinator/exams?examId=${demo.classic_exam_id}&attemptId=${demo.retake_attempt_id}`);
  await page.getByRole('heading', { name: 'Экзамен завершён', exact: true }).waitFor();
  await page.getByText('0 / 5', { exact: true }).first().waitFor();
  await page.getByRole('button', { name: 'Открыть историю всех вопросов', exact: true }).click();
  await page.getByRole('heading', { name: 'Протокол вопросов', exact: true }).waitFor();
  pass('optimized examiner opens its completed attempt by the selected exam and reads immutable protocol');
  await mobile(page, 'bff-exams-optimized-examiner-mobile.png');
  pass('optimized examiner protocol fits a 390px viewport');
  await context.close();
  assert.deepEqual(errors, []);
  assert.deepEqual(external, []);
  assert.deepEqual(forbiddenWrites, []);
  pass('five fresh HttpOnly cookie sessions; no JavaScript errors, remote traffic or exam writes');
  status = 'passed';
} catch (error) {
  failure = String(error);
  if (activePage && !activePage.isClosed()) {
    await fs.writeFile(output + 'bff-exams-optimized-failure.txt', await activePage.locator('body').innerText(), { mode: 0o600 }).catch(() => {});
  }
  throw error;
} finally {
  await fs.writeFile(output + 'bff-exams-optimized-report.json', JSON.stringify({ status, checks, errors, external, forbiddenWrites, ...(failure ? { error: failure } : {}), fixture_run: demo.run }, null, 2) + '\n', { mode: 0o600 });
  await browser.close();
  console.log(JSON.stringify({ status, checks: checks.length }));
}
