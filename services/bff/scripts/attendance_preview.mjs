// Read-only screenshots of the final clean local demo.
import {chromium} from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';import assert from 'node:assert/strict';
const root=new URL('../../../',import.meta.url).pathname,{password}=JSON.parse(await fs.readFile(root+'dev/local/accounts.json','utf8'));
const browser=await chromium.launch({executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
try{for(const [name,viewport] of [['desktop',{width:1440,height:1000}],['mobile',{width:390,height:844}]]){
 const context=await browser.newContext({viewport});await context.route('**/*',r=>new URL(r.request().url()).hostname==='127.0.0.1'?r.continue():r.abort());
 const page=await context.newPage();await page.goto('http://127.0.0.1:3001/login');await page.getByLabel('Логин',{exact:true}).fill('dev_student_01');await page.getByLabel('Пароль',{exact:true}).fill(password);await page.getByRole('button',{name:'Войти',exact:true}).click();await page.waitForURL('**/cabinet/**');await page.goto('http://127.0.0.1:3001/cabinet/student/schedule');await page.getByLabel('Дата',{exact:true}).fill('2026-11-15');await page.locator('details summary').first().click();await page.getByText('Загрузка расписания…').waitFor({state:'hidden'});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));await page.locator('details[open]').evaluate(el=>Promise.all(el.getAnimations({subtree:true}).map(animation=>animation.finished)));await page.screenshot({path:root+`dev/local/attendance-${name}.png`,fullPage:true});await context.close();
}}finally{await browser.close()}
console.log('Final desktop/mobile calendar screenshots saved; API read-only.');
