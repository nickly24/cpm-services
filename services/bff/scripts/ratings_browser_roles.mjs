/** Live local BFF checks. All data comes from API-created dev fixtures; no database access. */
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { chromium } from "/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs";
import * as XLSX from "../../../cpm_front_new/node_modules/xlsx/xlsx.mjs";

const root = new URL("../../../", import.meta.url).pathname;
const output = `${root}dev/local`;
const origin = "http://127.0.0.1:3001";
const readOnly = process.argv.includes("--read-only");
const reportPath = `${output}/${readOnly ? "bff-ratings-roles-optimized-report.json" : "bff-ratings-roles-browser-report.json"}`;
const screenshotPath = name => `${output}/${name}${readOnly ? "-optimized" : ""}.png`;
const demo = JSON.parse(await fs.readFile(`${output}/bff-ratings-demo.json`, "utf8"));
const { password } = JSON.parse(await fs.readFile(`${output}/accounts.json`, "utf8"));
const browser = await chromium.launch({ executablePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", headless: true });
const checks = [], errors = [], external = [], requests = [];
let context, page;
const pass = (name, details = {}) => { checks.push({ name, ...details }); console.log(`PASS ${name}`); };
const formatted = value => value.toLocaleString("ru-RU", { maximumFractionDigits: 6 });
const divisionA = demo.division_a ?? demo.division_id;
const divisionB = demo.division_b ?? demo.second_division_id;
const jobsA = demo.jobs_a ?? [];
const jobId = job => typeof job === "string" ? job : job.id ?? job.job_id;

async function login(loginName) {
  if (context) await context.close();
  context = await browser.newContext({ viewport: { width: 1440, height: 1050 }, serviceWorkers: "block" });
  await context.route("**/*", route => {
    const url = new URL(route.request().url());
    if (url.hostname !== "127.0.0.1") { external.push(`${url.protocol}//${url.hostname}${url.pathname}`); return route.abort(); }
    return route.continue();
  });
  page = await context.newPage();
  page.setDefaultTimeout(20000);
  page.on("pageerror", error => errors.push(error.message));
  page.on("request", request => {
    const url = new URL(request.url());
    if (url.pathname.startsWith("/bff/")) requests.push({ role: loginName, method: request.method(), path: url.pathname });
  });
  await page.goto(`${origin}/login`);
  await page.getByLabel("Логин", { exact: true }).fill(loginName);
  await page.getByLabel("Пароль", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await page.waitForURL("**/cabinet/**");
  await page.getByLabel("Переключить кабинет и подразделение").waitFor();
}

async function cabinet(division, organization) {
  const selector = page.getByLabel("Переключить кабинет и подразделение");
  const option = await selector.locator("option").evaluateAll((options, args) => options.find(option => option.value.endsWith(`:${args.division}`) && (!args.organization || option.value.startsWith(`${args.organization}:`)))?.value, { division, organization });
  assert.ok(option, "Requested cabinet unavailable");
  if (await selector.inputValue() !== option) {
    await selector.selectOption(option);
    await page.waitForFunction(value => document.querySelector('select[aria-label="Переключить кабинет и подразделение"]')?.value === value, option);
    await page.waitForTimeout(300);
  }
}

async function api(path, options = {}) {
  if (readOnly && path.startsWith("/ratings/") && options.method && options.method !== "GET")
    throw new Error("Readonly browser check must not send ratings mutations");
  const meResponse = await context.request.get(`${origin}/bff/auth/me`, { headers: { "X-CPM-Request": "1" } });
  assert.equal(meResponse.status(), 200);
  const identity = await meResponse.json();
  const response = await context.request.fetch(`${origin}/bff${path}`, { ...options, headers: {
    "X-CPM-Request": "1", "X-CPM-Context": identity.context_revision,
    ...(options.method && options.method !== "GET" ? { Origin: origin, "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() } : {}), ...options.headers,
  } });
  return { status: response.status(), body: await response.json() };
}
async function data(path) { const response = await api(path); assert.equal(response.status, 200, `${path}: ${response.status}`); return response.body.data; }
async function ratingsPage(role) { await page.goto(`${origin}/cabinet/${role}/ratings`); await page.getByRole("heading", { name: "Рейтинг", exact: true }).waitFor(); assert.equal(new URL(page.url()).pathname, `/cabinet/${role}/ratings`); }
async function finalScore(score) { await page.locator("dl > div").filter({ has: page.getByText("Итоговый рейтинг", { exact: true }) }).locator("dd").getByText(formatted(score), { exact: true }).waitFor(); }
async function waitRows() { await page.locator("tbody tr").first().waitFor(); }
async function noMutations() { assert.equal(await page.getByRole("button", { name: /^(Пересчитать|Отменить|Повторить пересчёт)/ }).count(), 0); }
async function mobile() {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.waitForTimeout(300);
  const geometry = await page.evaluate(() => ({ viewport: innerWidth, width: document.documentElement.scrollWidth }));
  assert.equal(geometry.width, geometry.viewport, JSON.stringify(geometry));
  return geometry;
}

try {
  assert.ok(divisionA && divisionB && demo.student_login && demo.proctor_login && demo.supervisor_login, "Missing completed fixture manifest");

  // Existing local student's two organizations also verify the never-calculated state.
  await login("dev_student_01");
  await page.goto(`${origin}/cabinet/student/performance`);
  await page.getByRole("heading", { name: "Мой рейтинг", exact: true }).waitFor();
  const initial = await data("/ratings/me");
  if (initial.freshness.reason === "never_calculated") {
    await page.getByText("Рейтинг в этом подразделении ещё не рассчитывался.", { exact: true }).waitFor();
    assert.equal(await page.locator("dl dd").count(), 0);
    pass("first publication has a distinct never-calculated state without fabricated zero");
  }
  const identity = (await api("/auth/me")).body;
  const currentOrg = identity.user.organization_id;
  const otherOption = await page.getByLabel("Переключить кабинет и подразделение").locator("option").evaluateAll((options, current) => options.find(option => Number(option.value.split(":")[0]) !== current)?.value, currentOrg);
  if (otherOption) {
    const [organization, division] = otherOption.split(":").map(Number);
    await cabinet(division, organization);
    const nextIdentity = (await api("/auth/me")).body;
    assert.equal(nextIdentity.user.organization_id, organization);
    assert.notEqual(nextIdentity.context_revision, identity.context_revision);
    const own = await data("/ratings/me");
    if (own.rating) assert.equal(own.rating.rating.organization_id, organization);
    pass("organization switch renews the context and restricts own rating to the chosen organization");
  }

  await login(demo.student_login); await cabinet(divisionA);
  await page.goto(`${origin}/cabinet/student/performance`);
  const own = await data("/ratings/me"); assert.ok(own.rating);
  assert.equal(own.rating.profile.account_id, demo.student_account_id);
  assert.equal(own.rating.rating.final_rating, demo.expected_a.final);
  await finalScore(demo.expected_a.final);
  await page.getByText(`Место в подразделении:`, { exact: false }).waitFor();
  await noMutations();
  pass("student sees the published weighted result and backend global place");
  await page.screenshot({ path: screenshotPath("bff-ratings-student"), fullPage: true });
  await page.route("**/bff/ratings/me", route => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ status: false, error: "upstream_unavailable" }) }), { times: 1 });
  await page.getByRole("button", { name: "Обновить рейтинг", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: /upstream_unavailable|Сервис временно недоступен/ }).waitFor(); await finalScore(demo.expected_a.final);
  await page.getByRole("button", { name: "Обновить рейтинг", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: /upstream_unavailable|Сервис временно недоступен/ }).waitFor({ state: "hidden" });
  pass("failed refresh keeps the last published score visible and explicit retry restores freshness");
  await page.getByRole("button", { name: "Что вошло в рейтинг", exact: true }).click();
  await page.getByRole("heading", { name: `Рейтинг: ${own.rating.profile.full_name}`, exact: true }).waitFor();
  const detailsRequest = requests.find(request => request.role === demo.student_login && request.path === `/bff/ratings/accounts/${demo.student_account_id}`);
  assert.ok(detailsRequest, "Details use account_id");
  await page.getByRole("region", { name: "Экзамены рейтинга" }).waitFor();
  const details = await data(`/ratings/accounts/${demo.student_account_id}?version=${own.rating.rating.job_id}`);
  assert.ok(details.rating.rating.exams.details.some(exam => exam.score === 0 && !exam.missing_result && exam.status === "Сдан"));
  assert.ok(details.rating.rating.exams.details.some(exam => exam.attempt_no === 2));
  pass("student details use account IDs and retain a completed zero retake");
  assert.ok([403, 404].includes((await api(`/ratings/accounts/${demo.student2_account_id}`)).status));
  for (const path of ["/ratings/jobs", "/ratings/versions", "/ratings/report"]) assert.equal((await api(path)).status, 403);
  pass("student cannot open another account, administrative jobs, versions or report");
  await cabinet(divisionB);
  assert.equal(await page.getByRole("heading", { name: `Рейтинг: ${own.rating.profile.full_name}`, exact: true }).count(), 0);
  await page.goto(`${origin}/cabinet/student/performance`);
  await finalScore(demo.expected_b.final);
  const ownB = await data("/ratings/me"); assert.equal(ownB.rating.rating.division_id, divisionB);
  assert.equal(ownB.rating.rating.final_rating, demo.expected_b.final);
  pass("switching subdivision while reading details clears the previous publication");

  await login(demo.student2_login); await cabinet(divisionA);
  await page.goto(`${origin}/cabinet/student/performance`); await finalScore(0);
  assert.equal((await data("/ratings/me")).rating.rating.final_rating, 0);
  pass("a real published final zero is visible for the second student");
  pass("student rating fits mobile viewport", await mobile());
  await page.screenshot({ path: screenshotPath("bff-ratings-student-mobile"), fullPage: true });

  await login(demo.proctor_login); await cabinet(divisionA); await ratingsPage("proctor"); await waitRows();
  const group = await data("/ratings/ratings?limit=200");
  assert.ok(group.ratings.length); assert.ok(group.ratings.every(row => row.profile.group_id === demo.group_a));
  assert.equal(group.ratings.some(row => row.profile.account_id === demo.student2_account_id), false);
  assert.equal(await page.getByRole("tab", { name: "Задания расчёта", exact: true }).count(), 0);
  assert.equal(await page.getByRole("button", { name: "Открыть отчёт", exact: true }).count(), 0);
  await noMutations();
  const proctorRow = page.locator("tbody tr").filter({ hasText: group.ratings[0].profile.full_name });
  assert.equal(await proctorRow.locator("td").first().innerText(), String(group.ratings[0].place));
  await proctorRow.getByRole("button", { name: `Рейтинг: ${group.ratings[0].profile.full_name}`, exact: true }).click();
  await page.getByRole("heading", { name: `Рейтинг: ${group.ratings[0].profile.full_name}`, exact: true }).waitFor();
  await page.getByRole("button", { name: "К рейтингу", exact: true }).click();
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Скачать отчёт группы", exact: true }).click();
  const download = await downloadPromise; const book = XLSX.read(await fs.readFile(await download.path()), { type: "buffer" });
  const sheet = XLSX.utils.sheet_to_json(book.Sheets[book.SheetNames[0]], { header: 1 });
  const accountColumn = sheet[0].indexOf("ID аккаунта"), placeColumn = sheet[0].indexOf("Место");
  assert.ok(accountColumn >= 0 && placeColumn >= 0);
  assert.deepEqual(sheet.slice(1).map(row => row[accountColumn]).sort(), group.ratings.map(row => row.profile.account_id).sort());
  for (const row of sheet.slice(1)) assert.equal(row[placeColumn], group.ratings.find(item => item.profile.account_id === row[accountColumn]).place);
  assert.ok([403, 404].includes((await api(`/ratings/accounts/${demo.student2_account_id}`)).status));
  for (const path of ["/ratings/jobs", "/ratings/versions", "/ratings/report"]) assert.equal((await api(path)).status, 403);
  pass("proctor table, own details and XLSX include only the assigned group with global places");
  assert.equal(requests.some(request => request.role === demo.proctor_login && request.path === "/bff/ratings/report"), false);
  await page.screenshot({ path: screenshotPath("bff-ratings-proctor"), fullPage: true });
  pass("proctor table fits mobile viewport", await mobile());

  await login(demo.supervisor_login); await cabinet(divisionA); await ratingsPage("supervisor"); await waitRows();
  const publication = await data("/ratings/ratings?limit=200");
  const zero = publication.ratings.find(row => row.profile.account_id === demo.student2_account_id); assert.ok(zero);
  await page.getByLabel("Поиск по рейтингу", { exact: true }).fill(zero.profile.full_name);
  await page.getByRole("button", { name: "Найти", exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll("tbody tr").length === 1);
  assert.equal(await page.locator("tbody tr td").first().innerText(), String(zero.place));
  assert.ok(zero.place > 1);
  pass("search keeps the zero student's global backend place instead of numbering filtered rows");
  await page.getByLabel("Поиск по рейтингу", { exact: true }).fill(""); await page.getByRole("button", { name: "Найти", exact: true }).click();
  const versions = (await data("/ratings/versions")).versions; assert.ok(versions.length >= 2);
  const previous = versions.find(version => version.revision === Math.min(...versions.map(item => item.revision))); assert.ok(previous);
  assert.equal(previous.id, jobId(jobsA.reduce((first, job) => !first || job.revision < first.revision ? job : first, null)));
  await page.getByLabel("Опубликованная версия", { exact: true }).selectOption(previous.id);
  const old = await data(`/ratings/ratings?version=${previous.id}&limit=200`);
  await page.locator("tbody tr").filter({ hasText: own.rating.profile.full_name }).getByText(formatted(old.ratings.find(row => row.profile.account_id === demo.student_account_id).rating.final_rating), { exact: true }).waitFor();
  await page.getByRole("button", { name: "Открыть отчёт", exact: true }).click();
  await page.locator('header[aria-label="Панель отчёта рейтинга"]').waitFor();
  const reportDownloadPromise = page.waitForEvent("download"); await page.getByRole("button", { name: "Excel", exact: true }).click();
  const reportDownload = await reportDownloadPromise; const reportBook = XLSX.read(await fs.readFile(await reportDownload.path()), { type: "buffer" });
  const reportRows = XLSX.utils.sheet_to_json(reportBook.Sheets[reportBook.SheetNames[0]], { header: 1 });
  assert.ok(reportRows.flat().includes(old.ratings.find(row => row.profile.account_id === demo.student_account_id).rating.final_rating));
  await page.screenshot({ path: screenshotPath("bff-ratings-supervisor-history"), fullPage: true });
  await page.getByRole("button", { name: "Назад", exact: true }).first().click();
  await page.getByRole("tab", { name: "Задания расчёта", exact: true }).click(); await waitRows(); await noMutations();
  if (!readOnly) assert.equal((await api("/ratings/jobs", { method: "POST", data: demo.period })).status, 403);
  pass("supervisor opens historical versions, exported report and readonly jobs without edit actions");
  await page.screenshot({ path: screenshotPath("bff-ratings-supervisor"), fullPage: true });

  // Admin-specific mutations are covered by the admin runner; this verifies shared read permissions.
  await login("dev_admin"); await cabinet(divisionA); await ratingsPage("admin"); await waitRows();
  await page.getByRole("button", { name: "Пересчитать", exact: true }).waitFor();
  assert.equal((await api("/ratings/report")).status, 200); pass("full administrator reads the publication and has recalculate controls");
  await cabinet(divisionB); await ratingsPage("admin"); await waitRows();
  assert.equal((await data("/ratings/ratings")).ratings.find(row => row.profile.account_id === demo.student_account_id).rating.final_rating, demo.expected_b.final);
  pass("administrator switches divisions without merging ratings");

  await login(demo.scoped_staff_view_login); await cabinet(divisionA); await ratingsPage("staff_admin"); await waitRows(); await noMutations();
  assert.equal((await api("/ratings/report")).status, 200);
  if (!readOnly) assert.equal((await api("/ratings/jobs", { method: "POST", data: demo.period })).status, 403);
  pass(readOnly ? "scoped staff viewer reads reports without recalculate controls" : "scoped staff viewer can read reports but has no recalculate controls or mutation permission");
  await login(demo.scoped_staff_editor_login); await cabinet(divisionA); await ratingsPage("staff_admin"); await waitRows();
  await page.getByRole("button", { name: "Пересчитать", exact: true }).waitFor();
  pass("scoped staff editor has ratings edit controls in the permitted division");

  assert.deepEqual(errors, []); assert.deepEqual(external, []);
  assert.equal(requests.some(request => request.path.startsWith("/bff/unconnected")), false);
  if (readOnly) assert.equal(requests.some(request => request.path.startsWith("/bff/ratings/") && request.method !== "GET"), false);
  pass("five roles made no legacy requests, external requests or browser errors");
  await fs.writeFile(reportPath, JSON.stringify({ result: "PASS", readOnly, skippedWriteDenials: readOnly ? 2 : 0, checks, errors, external, requests }, null, 2));
  console.log(JSON.stringify({ result: "PASS", checks: checks.length, readOnly, skippedWriteDenials: readOnly ? 2 : 0 }, null, 2));
} catch (error) {
  if (page) await page.screenshot({ path: screenshotPath("bff-ratings-roles-browser-failure"), fullPage: true });
  await fs.writeFile(reportPath, JSON.stringify({ result: "FAIL", readOnly, message: error.message, checks, errors, external, requests }, null, 2));
  console.error(error.stack); process.exitCode = 1;
} finally { await browser.close(); }
