/** Read-only local supervisor source report QA. No subject data mutations. */
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { chromium } from "/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs";
import * as XLSX from "../../../cpm_front_new/node_modules/xlsx/xlsx.mjs";

const root = new URL("../../../", import.meta.url).pathname;
const output = `${root}dev/local`;
const origin = "http://127.0.0.1:3001";
const demo = JSON.parse(await fs.readFile(`${output}/bff-ratings-demo.json`, "utf8"));
const { password } = JSON.parse(await fs.readFile(`${output}/accounts.json`, "utf8"));
const expected = JSON.parse(await fs.readFile(`${output}/bff-core-reports-http-report.json`, "utf8"));
assert.equal(expected.status, "passed");
const browser = await chromium.launch({ executablePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1050 }, serviceWorkers: "block", acceptDownloads: true });
const checks = [], errors = [], external = [], requests = [];
const pass = name => { checks.push(name); console.log(`PASS ${name}`); };
await context.route("**/*", route => {
  const url = new URL(route.request().url());
  if (url.hostname !== "127.0.0.1") { external.push(`${url.protocol}//${url.hostname}${url.pathname}`); return route.abort(); }
  if (url.pathname.startsWith("/bff/") && !["GET", "HEAD"].includes(route.request().method()) && !url.pathname.startsWith("/bff/auth/")) throw new Error("Read-only browser attempted a subject write");
  return route.continue();
});
const page = await context.newPage(); page.setDefaultTimeout(20000);
page.on("pageerror", error => errors.push(error.message));
page.on("request", request => { const url = new URL(request.url()); if (url.pathname.startsWith("/bff/")) requests.push({ method: request.method(), path: url.pathname, query: url.search }); });

async function report(kind) {
  await page.goto(`${origin}/cabinet/supervisor/${kind}`);
  await page.getByRole("button", { name: "Диапазон дат", exact: true }).click();
  await page.locator("#rep-from").fill(demo.period.date_from);
  await page.locator("#rep-to").fill(demo.period.date_to);
  await page.getByRole("button", { name: "Создать", exact: true }).click();
  await page.getByRole("heading", { name: { tests: "Результаты тестов", exams: "Результаты экзаменов", attendance: "Посещаемость" }[kind], exact: true }).waitFor();
  if (expected.reports[`${kind}_a`].items.length) await page.locator("tbody tr").first().waitFor();
  else await page.getByText("За выбранный период нет дней занятий.", { exact: true }).waitFor();
}
async function mobile() {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), 390);
  await page.screenshot({ path: `${output}/bff-core-reports-mobile.png`, fullPage: true });
  await page.setViewportSize({ width: 1440, height: 1050 });
}

try {
  await page.goto(`${origin}/login`);
  await page.getByLabel("Логин", { exact: true }).fill(demo.supervisor_login);
  await page.getByLabel("Пароль", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await page.waitForURL("**/cabinet/**");
  const selector = page.getByLabel("Переключить кабинет и подразделение");
  const option = await selector.locator("option").evaluateAll((options, division) => options.find(option => option.value.endsWith(`:${division}`))?.value, demo.division_a);
  assert.ok(option); if (await selector.inputValue() !== option) await selector.selectOption(option);
  const cookie = (await context.cookies()).find(cookie => cookie.name === "cpm_bff");
  assert.ok(cookie?.httpOnly && cookie.sameSite === "Strict");
  pass("supervisor uses the selected division and HttpOnly BFF cookie");

  await report("tests");
  const student = expected.reports.tests_a.students.find(row => row.account_id === demo.student_account_id);
  const student2 = expected.reports.tests_a.students.find(row => row.account_id === demo.student2_account_id);
  let row = page.locator("tbody tr").filter({ has: page.getByRole("rowheader", { name: student.full_name, exact: true }) });
  assert.equal(await row.locator('td[data-status="ready"]').getByText("0", { exact: true }).count(), 1);
  assert.equal(await row.locator('td[data-status="ready"]').getByText("100", { exact: true }).count(), 1);
  assert.equal(await row.getByText("Нет результата", { exact: true }).count(), 1);
  pass("independent test report displays official zero, 100 and missing as separate states");
  assert.equal(await page.locator("tbody tr").filter({ has: page.getByRole("rowheader", { name: student2.full_name, exact: true }) }).getByText("Нет результата", { exact: true }).count(), 3);
  await page.getByRole("combobox", { name: /^Группа/ }).selectOption(String(demo.group_a));
  assert.equal(await page.locator("tbody tr").count(), 1);
  await page.getByRole("combobox", { name: /^Группа/ }).selectOption("");
  await page.getByLabel("Поиск ученика", { exact: true }).fill(student2.full_name);
  assert.equal(await page.locator("tbody tr").count(), 1);
  await page.getByLabel("Поиск ученика", { exact: true }).fill("");
  pass("group and student search filter actual current student rows");
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Скачать Excel", exact: true }).click();
  const download = await downloadPromise; const workbook = XLSX.read(await fs.readFile(await download.path()), { type: "buffer" });
  const sheet = XLSX.utils.sheet_to_json(workbook.Sheets[workbook.SheetNames[0]], { header: 1 });
  const exported = sheet.find(row => row[0] === student.full_name);
  assert.ok(exported.includes(0) && exported.includes(100) && exported.includes("Нет результата"));
  pass("Excel source report retains numeric zero and textual missing without rating publication");
  await page.screenshot({ path: `${output}/bff-core-reports-tests.png`, fullPage: true });

  await report("exams");
  for (const item of expected.reports.exams_a.items) await page.getByRole("columnheader", { name: new RegExp(item.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")) }).waitFor();
  row = page.locator("tbody tr").filter({ has: page.getByRole("rowheader", { name: student.full_name, exact: true }) });
  const grades = await row.locator('td[data-status="completed"]').evaluateAll(cells => cells.map(cell => cell.firstElementChild?.tagName === "SPAN" ? cell.firstElementChild.textContent : cell.childNodes[0]?.textContent));
  assert.deepEqual(grades, ["0", "2"]);
  await row.getByText("Попытка 2", { exact: true }).waitFor();
  await page.getByText(/Вне системы/).first().waitFor();
  pass("exam source report shows true names, effective worse retake0 and outside grade2");
  await mobile(); pass("report scrolls its table within the mobile viewport without page overflow");
  await page.screenshot({ path: `${output}/bff-core-reports-exams.png`, fullPage: true });

  await report("attendance");
  await page.getByText("Отметки посещаемости за период. Отсутствие отметки означает «Не отмечено».", { exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "Скачать Excel", exact: true }).isDisabled(), true);
  pass("current attendance owner empty period has no fabricated marks or enabled empty export");
  await page.getByRole("button", { name: "Изменить период", exact: true }).click();
  await page.getByRole("button", { name: "Выбрать период и сформировать отчёт", exact: true }).waitFor();
  pass("report period can be changed without keeping old result rows");
  assert.equal(external.length, 0); assert.equal(errors.length, 0);
  assert.ok(["tests", "exams", "attendance"].every(kind => requests.some(request => request.path === `/bff/core/reports/${kind}`)));
  assert.ok(!requests.some(request => request.path === "/bff/ratings/report"));
  pass("all three supervisor reports read Core through BFF without external, legacy rating or console errors");
  await fs.writeFile(`${output}/bff-core-reports-browser-report.json`, JSON.stringify({ status: "passed", count: checks.length, checks, errors, external, requests }, null, 2));
} catch (error) {
  await page.screenshot({ path: `${output}/bff-core-reports-browser-failure.png`, fullPage: true }).catch(() => {});
  await fs.writeFile(`${output}/bff-core-reports-browser-report.json`, JSON.stringify({ status: "failed", count: checks.length, checks, error: error.message, errors, external, requests }, null, 2));
  throw error;
} finally { await context.close(); await browser.close(); }
