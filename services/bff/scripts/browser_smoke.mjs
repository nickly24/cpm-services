import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
const root=new URL('../../../',import.meta.url).pathname;
const {password}=JSON.parse(await fs.readFile(root+'dev/local/accounts.json','utf8'));
const browser=await chromium.launch({executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
const context=await browser.newContext({viewport:{width:1440,height:1000}});
const external=[];const errors=[];const requests=[];
await context.route('**/*',route=>{const url=new URL(route.request().url());if(!['127.0.0.1','localhost'].includes(url.hostname)){external.push(url.hostname);return route.abort();}return route.continue();});
const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>requests.push({method:r.method(),path:new URL(r.url()).pathname}));
try{
 await page.goto('http://127.0.0.1:3001/login');
 await page.getByLabel('Логин',{exact:true}).fill('dev_student_01');await page.getByLabel('Пароль',{exact:true}).fill(password);await page.getByRole('button',{name:'Войти',exact:true}).click();
 await page.waitForURL('**/cabinet/student/homework');await page.getByRole('heading',{name:'Домашние задания',exact:true}).waitFor();
 const demo=JSON.parse(await fs.readFile(root+"dev/local/bff-demo.json","utf8"));
 await page.goto("http://127.0.0.1:3001/cabinet/student/homework?work="+demo.file_homework); await page.getByRole("dialog").waitFor();
 await page.waitForTimeout(1500);
 await page.screenshot({path:root+'dev/local/bff-student.png',fullPage:true});
 console.log((await page.locator('body').innerText()).slice(0,6500));
 console.log(JSON.stringify({errors,external,bff:requests.filter(r=>r.path.startsWith('/bff'))}));
}finally{await browser.close();}
