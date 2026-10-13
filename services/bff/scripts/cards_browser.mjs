/** Repeatable local Cards UI checks. Writes touch only API-created, synthetic dev fixtures. */
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { chromium } from "/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs";

const workspace = new URL("../../../", import.meta.url).pathname, output = `${workspace}dev/local`, origin = "http://127.0.0.1:3001";
const devReadOnly = process.argv.includes("--dev-read-only");
const readOnly = process.argv.includes("--read-only") || devReadOnly;
const mode = devReadOnly ? "read-only-dev" : readOnly ? "optimized" : "browser";
const demo = JSON.parse(await fs.readFile(`${output}/bff-cards-demo.json`, "utf8"));
const { password: adminPassword } = JSON.parse(await fs.readFile(`${output}/accounts.json`, "utf8"));
const { password: cardsPassword } = JSON.parse(await fs.readFile(`${workspace}${demo.credential_file ?? "dev/local/accounts.json"}`, "utf8"));
const checks = [], errors = [], remote = [], requests = [], alerts = [];
const browser = await chromium.launch({ executablePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", headless: true });
let context, page, activeLogin;
let own = readOnly ? JSON.parse(await fs.readFile(`${output}/bff-cards-browser-fixture.json`, "utf8")) : {};
const pass = (name, details = {}) => { checks.push({ name, ...details }); console.log(`PASS ${name}`); };
const aliases = { admin: demo.admin_login ?? "physics", editor: demo.editor_login ?? "dev_cards_editor", viewer: demo.viewer_login ?? "dev_cards_viewer", student: demo.student_login ?? "dev_cards_student" };
const divisionA = demo.division_a ?? demo.division_id, divisionB = demo.division_b ?? demo.second_division_id;

async function login(kind, override) {
  if (context) await context.close();
  activeLogin = override?.login ?? aliases[kind];
  context = await browser.newContext({ viewport: { width: 1440, height: 1050 }, serviceWorkers: "block" });
  await context.route("**/*", route => { const url = new URL(route.request().url()); if (url.hostname !== "127.0.0.1") { remote.push(`${url.hostname}${url.pathname}`); return route.abort(); } return route.continue(); });
  page = await context.newPage(); page.setDefaultTimeout(25000);
  page.on("pageerror", error => errors.push(error.message));
  page.on("request", request => { const url = new URL(request.url()); if (url.pathname.startsWith("/bff/")) requests.push({ login: activeLogin, method: request.method(), path: url.pathname }); });
  page.on("dialog", async dialog => { if (dialog.type() === "confirm" || dialog.type() === "beforeunload") await dialog.accept(); else { alerts.push(dialog.message()); await dialog.dismiss(); } });
  await page.goto(`${origin}/login`); await page.getByLabel("Логин", { exact: true }).fill(activeLogin); await page.getByLabel("Пароль", { exact: true }).fill(override?.password ?? (kind === "admin" ? adminPassword : cardsPassword)); await page.getByRole("button", { name: "Войти", exact: true }).click(); await page.waitForURL("**/cabinet/**"); await page.getByLabel("Переключить кабинет и подразделение").waitFor();
  if (override?.division ?? divisionA) await cabinet(override?.division ?? divisionA);
}
async function cabinet(division) {
  const selector = page.getByLabel("Переключить кабинет и подразделение");
  const value = await selector.locator("option").evaluateAll((items, division) => items.find(item => item.value.endsWith(`:${division}`))?.value, division);
  assert.ok(value, `Missing requested cabinet ${division}`);
  if (await selector.inputValue() !== value) { await selector.selectOption(value); await page.waitForFunction(value => document.querySelector('select[aria-label="Переключить кабинет и подразделение"]')?.value === value, value); await page.waitForTimeout(350); }
}
async function api(path, options = {}) {
  if (readOnly && !path.startsWith("/auth/") && options.method && options.method !== "GET") throw new Error("Optimized Cards QA is readonly");
  const identity = await (await context.request.get(`${origin}/bff/auth/me`, { headers: { "X-CPM-Request": "1" } })).json();
  const response = await context.request.fetch(`${origin}/bff${path}`, { ...options, headers: { "X-CPM-Request": "1", "X-CPM-Context": identity.context_revision, ...(options.method && options.method !== "GET" ? { Origin: origin, "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() } : {}), ...options.headers } });
  return { status: response.status(), body: await response.json() };
}
async function train(role) { await page.goto(`${origin}/cabinet/${role}/train`); await page.getByRole("heading", { name: "Карточки", exact: true }).waitFor(); }
async function adminDirection() { await page.getByRole("button").filter({ has: page.getByRole("heading", { name: demo.direction_name, exact: true }) }).click(); }
async function adminTheme(name) { await page.getByRole("button").filter({ has: page.getByRole("heading", { name, exact: true }) }).click(); }
async function mobile(name) { await page.setViewportSize({ width: 390, height: 844 }); await page.waitForTimeout(350); await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))); const bounds = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth })); assert.ok(bounds.document <= bounds.viewport + 1, JSON.stringify(bounds)); await page.screenshot({ path: `${output}/bff-cards-${name}${readOnly ? `-${mode}` : ""}.png`, fullPage: true }); await page.setViewportSize({ width: 1440, height: 1050 }); pass(`${name} mobile width`, bounds); }
async function lostReply(pattern, method, fn) {
  let dropped = false; const keys = [];
  await page.route(pattern, async route => { if (route.request().method() !== method) return route.continue(); keys.push(route.request().headers()["idempotency-key"]); if (!dropped) { dropped = true; await route.fetch(); return route.abort("failed"); } return route.continue(); });
  await fn(); assert.ok(dropped); await page.unroute(pattern); assert.ok(keys[0] && keys.every(key => key === keys[0]), "Retry changed logical command UUID"); return keys;
}

try {
  await login("admin"); await train("admin"); await adminDirection();
  const catalog = await api("/cards/admin/catalog"); assert.equal(catalog.status, 200); const direction = catalog.body.data.directions.find(item => item.id === demo.direction_id); assert.ok(direction); assert.ok(direction.sections.some(item => item.kind === "manual"));
  if (demo.hidden_test_id) { const hidden = direction.sections.find(item => item.test_id === demo.hidden_test_id); assert.equal(hidden?.visible, false); await page.getByText("ответы скрыты у студентов", { exact: false }).first().waitFor(); }
  pass("Admin manual/test catalog and owner visibility metadata");
  if (!readOnly) {
    own.name = `UI карточки ${demo.run ?? "dev"}-${Date.now().toString(16)}`;
    await page.getByRole("button", { name: "Новый раздел", exact: true }).click(); await page.getByLabel("Название раздела").fill(own.name);
    const selector = page.getByLabel("Переключить кабинет и подразделение"); const originalCabinet = await selector.inputValue();
    const other = await selector.locator("option").evaluateAll(items => items.find(item => item.value !== document.querySelector('select[aria-label="Переключить кабинет и подразделение"]')?.value)?.value);
    if (other) { await selector.selectOption(other); await page.getByText("Закройте редактор", { exact: false }).waitFor(); await page.getByRole("dialog").waitFor(); assert.equal(await selector.inputValue(), originalCabinet); }
    const keys = await lostReply("**/bff/cards/themes", "POST", async () => { await page.getByRole("dialog").getByRole("button", { name: "Сохранить", exact: true }).click(); await page.getByText("lost", { exact: false }).count(); await page.getByRole("dialog").getByRole("button", { name: "Повторить сохранение" }).waitFor(); assert.equal(await page.getByLabel("Название раздела").isDisabled(), true); await page.getByRole("dialog").getByRole("button", { name: "Повторить сохранение" }).click(); await page.getByRole("dialog").waitFor({ state: "hidden" }); });
    const after = await api("/cards/admin/catalog"); const rows = after.body.data.directions.flatMap(item => item.sections).filter(item => item.kind === "manual" && item.name === own.name); assert.equal(rows.length, 1); own.theme_id = rows[0].id; pass("Editor guard and lost create replay without duplicate", { repeats: keys.length });
    await adminTheme(own.name); await page.getByRole("heading", { name: "Карточек пока нет", exact: true }).waitFor(); pass("Empty theme preserves zero cards");
    await page.getByRole("button", { name: "Новая карточка", exact: true }).click(); await page.getByLabel("Вопрос", { exact: true }).fill("Новый вопрос UI"); await page.getByLabel("Ответ", { exact: true }).fill("Полный ответ UI\nВторая строка"); await page.getByRole("dialog").getByRole("button", { name: "Сохранить", exact: true }).click(); await page.getByRole("dialog").waitFor({ state: "hidden" }); await page.getByText("Новый вопрос UI", { exact: true }).waitFor();
    own.card = (await api(`/cards/themes/${own.theme_id}/cards`)).body.data[0]; assert.equal(own.card.answer, "Полный ответ UI\nВторая строка"); assert.equal(own.card.revision, 1); pass("Manual card create preserves answer and owner revision");
    await page.getByRole("button", { name: "Редактировать", exact: true }).click(); await page.getByLabel("Ответ", { exact: true }).fill("Новый ответ UI\nВторая строка"); await page.getByRole("dialog").getByRole("button", { name: "Сохранить", exact: true }).click(); await page.getByRole("dialog").waitFor({ state: "hidden" }); own.card = (await api(`/cards/themes/${own.theme_id}/cards`)).body.data[0]; assert.equal(own.card.revision, 2); pass("Manual card update uses CAS revision");
    await page.getByRole("button", { name: "Трансформировать в тест", exact: true }).click(); const commitKeys = await lostReply("**/bff/cards/sessions/*/commit", "POST", async () => { await page.getByRole("dialog").getByRole("button", { name: "Создать черновик", exact: true }).click(); await page.getByRole("dialog").getByRole("button", { name: "Повторить создание" }).waitFor(); await page.getByRole("dialog").getByRole("button", { name: "Повторить создание" }).click(); await page.waitForURL("**/cabinet/admin/tests"); });
    const drafts = await api("/tests/drafts"); const draftRows = drafts.body.data.items ?? drafts.body.data.drafts ?? drafts.body.data; const matches = draftRows.filter(item => item.title === `${own.name} — тест`); assert.equal(matches.length, 1); own.draft_id = matches[0].id; const full = await api(`/tests/drafts/${own.draft_id}`); assert.equal(full.body.data.questions[0].correct_answers[0], own.card.answer); pass("Transform lost commit retries one real test draft, without Upload", { repeats: commitKeys.length });
    await fs.writeFile(`${output}/bff-cards-browser-fixture.json`, JSON.stringify(own, null, 2));
  } else { assert.ok(own.theme_id); pass("Optimized read-only checks reuse confirmed CRUD and transform fixtures"); }

  await login("student"); await train("student"); await page.getByRole("button", { name: demo.direction_name, exact: true }).click(); await page.waitForURL(`**/train/${encodeURIComponent(demo.direction_name)}`); await page.waitForTimeout(350); await page.getByLabel("Найти раздел").fill(own.name); await page.getByRole("heading", { name: own.name, exact: true }).waitFor();
  const tree = await api("/cards/tree"); assert.equal(tree.status, 200); assert.ok(tree.body.data.directions.flatMap(item => item.sections).some(item => item.kind === "manual" && item.refId === String(own.theme_id))); if (demo.hidden_test_id) assert.ok(!tree.body.data.directions.flatMap(item => item.sections).some(item => item.refId === demo.hidden_test_id)); pass("Student tree and hidden test privacy");
  await page.getByRole("button").filter({ has: page.getByRole("heading", { name: own.name, exact: true }) }).click(); await page.waitForURL(`**/train/manual/${own.theme_id}`); await page.getByText(own.card.answer, { exact: true }).waitFor(); pass("Stable manual URL loads own card detail");
  if (!readOnly) {
    await page.getByRole("button", { name: "Настройки обучения", exact: true }).click(); await page.getByRole("button", { name: "10 карточек", exact: true }).click(); await page.getByRole("button", { name: "20 карточек", exact: true }).click(); await page.waitForFunction(() => !document.body.innerText.includes("Сохранение настроек"));
    await page.getByRole("button", { name: "Пометить как выученное", exact: true }).click(); await page.getByRole("button", { name: "Пометить как невыученное", exact: true }).waitFor(); const learned = (await api(`/cards/sections/manual/${own.theme_id}`)).body.data; assert.equal(learned.stats.learned, 1); assert.equal(learned.settings.batch_size, 20); pass("Student settings and fingerprint progress confirmed by owner");
    await page.getByRole("button", { name: "Пометить как невыученное", exact: true }).click(); await page.getByRole("button", { name: "Пометить как выученное", exact: true }).waitFor();
    await page.getByRole("button", { name: /^Все карточки/ }).click(); await page.getByRole("button", { name: "Учить все карточки раздела", exact: true }).click(); await page.waitForURL(`**/train/manual/${own.theme_id}/study?**`);
    const onboarding = page.getByRole("button", { name: "Начать", exact: true }); await page.getByRole("button", { name: "Знаю", exact: true }).waitFor(); if (await onboarding.isVisible()) await onboarding.click();
    await page.getByText(own.card.question, { exact: true }).click(); await page.getByText(own.card.answer, { exact: true }).waitFor();
    const progressKeys = await lostReply(`**/bff/cards/sections/manual/${own.theme_id}/learned`, "POST", async () => { await page.getByRole("button", { name: "Знаю", exact: true }).click(); await page.getByText("Не удалось сохранить прогресс. Повторите попытку.", { exact: true }).waitFor(); await page.getByRole("button", { name: "Знаю", exact: true }).click(); await page.getByRole("heading", { name: "Занятие завершено", exact: true }).waitFor(); });
    await page.getByText("Прогресс области: 1 из 1 выучено · 100%", { exact: true }).waitFor(); assert.equal((await api(`/cards/sections/manual/${own.theme_id}`)).body.data.stats.learned, 1); pass("Flashcard flip/study and lost progress reply recover without duplicate learning", { repeats: progressKeys.length });
    await page.getByRole("button", { name: "К разделу", exact: true }).click(); await page.waitForURL(`**/train/manual/${own.theme_id}`); await page.getByRole("heading", { name: own.name, exact: true }).waitFor();
  }
  const before = (await api(`/cards/sections/manual/${own.theme_id}`)).body.data;
  if (divisionB) { await cabinet(divisionB); await page.goto(`${origin}/cabinet/student/train/manual/${own.theme_id}`); await page.getByRole("heading", { name: own.name, exact: true }).waitFor(); const after = (await api(`/cards/sections/manual/${own.theme_id}`)).body.data; assert.deepEqual(after.stats, before.stats); assert.deepEqual(after.settings, before.settings); pass("Division switching retains account/org progress and settings"); await cabinet(divisionA); }
  const forbidden = await api(`/cards/themes/${own.theme_id}/cards`); assert.equal(forbidden.status, 403); pass("Student cannot open administrative cards endpoint");
  const selector = page.getByLabel("Переключить кабинет и подразделение");
  const foreign = await selector.locator("option").evaluateAll((items, organization) => items.find(item => !item.value.startsWith(`${organization}:`))?.value, demo.organization_id);
  if (foreign) { await selector.selectOption(foreign); await page.waitForFunction(value => document.querySelector('select[aria-label="Переключить кабинет и подразделение"]')?.value === value, foreign); await page.goto(`${origin}/cabinet/student/train/manual/${own.theme_id}`); assert.equal((await api(`/cards/sections/manual/${own.theme_id}`)).status, 404); assert.equal(await page.getByText(own.card.answer, { exact: true }).count(), 0); pass("Organization switching removes foreign cards and progress"); await cabinet(divisionA); }
  if (demo.eligible_test_id) { await page.goto(`${origin}/cabinet/student/train/test/${demo.eligible_test_id}`); await page.getByText("На базе теста", { exact: false }).count(); const view = await api(`/cards/sections/test/${demo.eligible_test_id}`); assert.equal(view.status, 200); assert.ok(view.body.data.cards.length); await page.getByText(view.body.data.cards[0].question, { exact: true }).waitFor(); pass("Eligible test cards use owner disclosure policy and fingerprint"); }
  const examCatalog = await api("/exams?scope=division&type=classic&limit=200&offset=0");
  let learnerExam;
  if (examCatalog.status === 200) for (const exam of examCatalog.body.data.items) { const training = await api(`/exams/${exam.id}/training`); if (training.status === 200) { learnerExam = exam; break; } }
  if (learnerExam) { await page.goto(`${origin}/cabinet/student/train/exam/${learnerExam.id}`); await page.getByRole("button", { name: "Учить весь экзамен", exact: true }).waitFor(); pass("Existing exam preparation remains reachable", { exam_id: learnerExam.id }); }
  await page.goto(`${origin}/cabinet/student/train/manual/${own.theme_id}`); await page.getByRole("heading", { name: own.name, exact: true }).waitFor(); await mobile("student");
  await login("viewer"); await train("staff_admin"); assert.ok(page.url().includes("/cabinet/staff_admin/train")); await adminDirection(); await adminTheme(own.name); await page.getByText(own.card.question, { exact: true }).waitFor(); assert.equal(await page.getByRole("button", { name: "Новая карточка", exact: true }).count(), 0); assert.equal(await page.getByRole("button", { name: "Редактировать", exact: true }).count(), 0); assert.equal(await page.getByRole("button", { name: "Удалить", exact: true }).count(), 0); assert.equal(await page.getByRole("button", { name: "Трансформировать в тест", exact: true }).count(), 0); pass("View staff sees catalog/cards and no mutations"); await mobile("viewer");
  await login("editor"); await train("staff_admin"); assert.ok(page.url().includes("/cabinet/staff_admin/train")); await adminDirection(); await adminTheme(own.name); await page.getByRole("button", { name: "Новая карточка", exact: true }).waitFor(); pass("Editor staff actions follow selected cabinet rights"); await mobile("editor");
  if (!learnerExam) {
    const exams = JSON.parse(await fs.readFile(`${output}/bff-exams-demo.json`, "utf8"));
    await login("student", { login: exams.student_login, password: adminPassword, division: exams.division_id });
    await train("student"); await page.getByRole("button", { name: "Фильтры", exact: false }).waitFor();
    const available = await api(`/exams/${exams.classic_exam_id}/training`); assert.equal(available.status, 200); assert.ok(available.body.data.cards.length);
    const detail = await api(`/exams/${exams.classic_exam_id}`), directions = await api("/core/directions");
    const examDirection = directions.body.data.find(item => item.id === detail.body.data.direction_id); assert.ok(examDirection);
    await page.getByRole("button", { name: examDirection.name, exact: true }).click(); await page.waitForURL(`**/train/${encodeURIComponent(examDirection.name)}`); await page.waitForTimeout(350); await page.getByLabel("Найти раздел").fill(detail.body.data.name); await page.getByRole("heading", { name: detail.body.data.name, exact: true }).waitFor();
    // Navigation verifies the preserved exam component; no exam content or progress is changed.
    await page.goto(`${origin}/cabinet/student/train/exam/${exams.classic_exam_id}`); await page.getByRole("button", { name: "Учить весь экзамен", exact: true }).waitFor();
    pass("Existing exam tree and learner preparation remain reachable after Cards migration", { exam_id: exams.classic_exam_id });
  }
  assert.equal(errors.length, 0, JSON.stringify(errors)); assert.equal(remote.length, 0, JSON.stringify(remote)); assert.ok(!requests.some(item => item.path.includes("/unconnected/") || item.path.includes("/imports/") || item.path.includes("/upload")), "Legacy or excluded import endpoint requested"); pass("No browser errors, remote legacy calls or global Upload requests");
} catch (error) { await page?.screenshot({ path: `${output}/bff-cards-browser-failure${readOnly ? `-${mode}` : ""}.png`, fullPage: true }); console.error(error); process.exitCode = 1; }
finally { await fs.writeFile(`${output}/bff-cards-${mode}-report.json`, JSON.stringify({ status: process.exitCode ? "failed" : "passed", check_count: checks.length, mode, readOnly, checks, errors, remote, requests, alerts, fixture: own }, null, 2)); await context?.close(); await browser.close(); }
