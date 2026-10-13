/** Joint local BFF/UI acceptance. No native owner URLs, tokens, DB, or runtime changes. */
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import crypto from 'node:crypto';
import { createRequire } from 'node:module';
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
export const root=new URL('../../../',import.meta.url).pathname, base='http://127.0.0.1:3001', output=root+'dev/local/';
const require=createRequire(root+'cpm_front_new/package.json'), XLSX=require('xlsx');
export const password=JSON.parse(await fs.readFile(output+'accounts.json','utf8')).password;
export function workbook(rows){const book=XLSX.utils.book_new();XLSX.utils.book_append_sheet(book,XLSX.utils.aoa_to_sheet(rows),'Synthetic');return XLSX.write(book,{bookType:'xlsx',type:'buffer'});}
export function workbookRows(buffer){const book=XLSX.read(buffer,{type:'buffer'});return XLSX.utils.sheet_to_json(book.Sheets[book.SheetNames[0]],{header:1,defval:''});}
export async function harness(label, writePolicy){
  const browser=await chromium.launch({executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
  const checks=[],errors=[],remote=[],legacy=[],requests=[],blockedWrites=[],businessWrites=[],cleanupErrors=[];let context,page,identity;
  function checkWrite(path,method,headers={}){
    if(['GET','HEAD'].includes(method)||path.startsWith('/bff/auth/'))return;
    if(!writePolicy)return;
    assert.equal(identity?.user.account_id,writePolicy.actorId,'Business write actor must match retained editor');
    assert.equal(identity?.user.organization_id,writePolicy.organizationId,'Business write organization must match retained scope');
    assert.equal(identity?.user.division_id,writePolicy.divisionId,'Business write division must match retained scope');
    assert(Date.now()<Date.parse(writePolicy.endsAt),'Business writes stop at the authorized window deadline');
    const key=headers['idempotency-key'];assert(key,'Every preview command needs its saved UUID');
    assert(!businessWrites.some(row=>row.key===key),'Remaining acceptance never replays an unknown command');
    if(path==='/bff/uploads/sessions'&&method==='POST'){
      assert((writePolicy.alreadyCreated??0)+businessWrites.filter(row=>row.method==='POST').length<writePolicy.maxSessions,'Cumulative remaining preview creation budget exhausted');
    }else{
      const match=path.match(/^\/bff\/uploads\/sessions\/([a-f0-9]{32})$/);
      assert(method==='PUT'&&match&&writePolicy.editableSessions.has(match[1]),'Only the two explicitly selected own previews may be edited');
      assert((writePolicy.alreadyEdited??0)+businessWrites.filter(row=>row.method==='PUT').length<writePolicy.maxEdits,'Cumulative remaining structured edit budget exhausted');
      assert(!businessWrites.some(row=>row.method==='PUT'&&row.path===path),'Remaining acceptance issues at most one CAS edit per selected preview');
    }
    businessWrites.push({method,path,key});
  }
  async function closeSession(){
    if(!context||!identity)return;
    try{const res=await context.request.post(base+'/bff/auth/logout',{headers:{Origin:base,'X-CPM-Request':'1','X-CPM-Context':identity.context_revision}});assert.equal(res.status(),200);}
    catch(error){cleanupErrors.push(error.name+': Auth session close not confirmed');}
    identity=undefined;
  }
  const pass=(name,details={})=>{checks.push({name,...details});console.log(`PASS ${name}`);};
  async function login(name='dev_admin',division){
    if(writePolicy)await closeSession();await context?.close();context=await browser.newContext({viewport:{width:1440,height:1000},acceptDownloads:true,serviceWorkers:'block'});
    await context.route('**/*',route=>{const url=new URL(route.request().url());if(url.origin!==base){remote.push(url.origin+url.pathname);return route.abort();}if(url.pathname.startsWith('/api/')||url.pathname.startsWith('/bff/unconnected'))legacy.push(url.pathname);try{checkWrite(url.pathname,route.request().method(),route.request().headers());}catch(error){blockedWrites.push({method:route.request().method(),path:url.pathname,reason:error.message});return route.abort('blockedbyclient');}return route.continue();});
    page=await context.newPage();page.setDefaultTimeout(20000);page.on('pageerror',e=>errors.push(e.message));page.on('dialog',d=>d.accept());
    page.on('request',r=>{const path=new URL(r.url()).pathname;if(path.startsWith('/bff/'))requests.push({method:r.method(),path});});
    await page.goto(base+'/login');await page.getByLabel('Логин',{exact:true}).fill(name);await page.getByLabel('Пароль',{exact:true}).fill(password);await page.getByRole('button',{name:'Войти',exact:true}).click();await page.waitForURL('**/cabinet/**');await page.getByLabel('Переключить кабинет и подразделение').waitFor();
    identity=await (await context.request.get(base+'/bff/auth/me',{headers:{'X-CPM-Request':'1'}})).json();
    if(division)await cabinet(division);
  }
  async function cabinet(division){
    const select=page.getByLabel('Переключить кабинет и подразделение'),value=await select.locator('option').evaluateAll((rows,id)=>rows.find(row=>row.value.endsWith(':'+id))?.value,division);assert(value,`No requested cabinet ${division}`);
    if(await select.inputValue()!==value){await select.selectOption(value);await page.waitForFunction(value=>document.querySelector('select[aria-label="Переключить кабинет и подразделение"]')?.value===value,value);await page.waitForLoadState('networkidle');}
    identity=await (await context.request.get(base+'/bff/auth/me',{headers:{'X-CPM-Request':'1'}})).json();
  }
  async function call(path,method='GET',body,expected=200,key=crypto.randomUUID()){
    assert(path.startsWith('/'));identity=await (await context.request.get(base+'/bff/auth/me',{headers:{'X-CPM-Request':'1'}})).json();
    checkWrite('/bff'+path,method,{'idempotency-key':key});
    const res=await context.request.fetch(base+'/bff'+path,{method,headers:{Origin:base,'X-CPM-Request':'1','X-CPM-Context':identity.context_revision,...(method!=='GET'?{'Idempotency-Key':key}: {})},...(body===undefined?{}:{data:body})});
    assert((Array.isArray(expected)?expected:[expected]).includes(res.status()),`${method} ${path}: ${res.status()}`);const json=await res.json();return json.data??json;
  }
  async function open(section,role=identity.user.role){await page.goto(`${base}/cabinet/${role}/${section}`);await page.waitForLoadState('networkidle');}
  async function mobile(name){await page.setViewportSize({width:390,height:844});await page.waitForTimeout(350);await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));const bounds=await page.evaluate(()=>({viewport:innerWidth,document:document.documentElement.scrollWidth}));assert(bounds.document<=bounds.viewport+1,JSON.stringify(bounds));await page.screenshot({path:output+`bff-${label}-${name}-mobile.png`,fullPage:true});await page.setViewportSize({width:1440,height:1000});pass(name+' fits mobile',bounds);}
  async function finish(status,fixture){if(writePolicy)await closeSession();const reportPath=output+`bff-${label}-report.json`;await fs.writeFile(reportPath,JSON.stringify({status:cleanupErrors.length?'failed':status,checks,check_count:checks.length,errors,remote,legacy,requests,blockedWrites,businessWrites,cleanupErrors,fixture},null,2),{mode:0o600});await fs.chmod(reportPath,0o600);await page?.unrouteAll({behavior:'wait'});await context?.close();await browser.close();}
  return {get page(){return page;},get context(){return context;},get identity(){return identity;},checks,errors,remote,legacy,requests,blockedWrites,businessWrites,pass,login,cabinet,call,open,mobile,finish};
}
export const independent=process.argv.includes('--independent');
const fixturePath=output+(independent?'bff-uploads-delta-independent-demo.json':'bff-uploads-delta-demo.json');
export async function fixture(h){
  try{const demo=JSON.parse(await fs.readFile(fixturePath,'utf8'));assert.equal(demo.state,'ready','Existing unknown/partial fixture must be recovered, never automatically recreated');return demo;}catch(e){if(e.code!=='ENOENT'||process.argv.includes('--resume-remaining')||process.argv.includes('--read-only'))throw e;}
  await h.login();const run=crypto.randomBytes(4).toString('hex'),prefix='DEV Joint '+run;
  const demo={run,prefix,state:'preparing',organization_id:h.identity.user.organization_id,commands:[]};
  const save=()=>fs.writeFile(fixturePath,JSON.stringify(demo,null,2),{mode:0o600});await save();
  async function create(path,body,expected=201){const entry={path,id:crypto.randomUUID(),state:'pending'};demo.commands.push(entry);await save();const result=await h.call(path,'POST',body,expected,entry.id);entry.state='confirmed';await save();return result;}
  demo.division_a=(await create('/core/divisions',{name:prefix+' A'})).id;demo.division_b=(await create('/core/divisions',{name:prefix+' B'})).id;
  const capabilities=['tests','test-results','upload','users','train','exams'];
  for(const kind of ['editor','viewer','results_only']){const sections=kind==='results_only'?['test-results']:capabilities;demo[kind]={role_id:(await create('/core/roles',{name:prefix+' '+kind,permissions:Object.fromEntries(sections.map(section=>[section,{view:true,edit:kind==='editor'}]))})).id,login:`joint_${run}_${kind}`,full_name:prefix+' '+kind};}
  await h.page.reload();await h.page.waitForLoadState('networkidle');await h.cabinet(demo.division_a);
  const dirs=await h.call('/core/directions');const direction=dirs.find(d=>d.id===1001)||dirs.find(d=>d.id===1002);assert(direction,'Existing direction 1001/1002 required');demo.direction_id=direction.id;demo.direction_name=direction.name;
  for(const kind of ['editor','viewer','results_only','student','student2']){
    const student=kind.startsWith('student'),details=demo[kind]??{login:`joint_${run}_${kind}`,full_name:independent?`Joint${run}${kind} Ученик`: `${prefix} ${kind==='student'?'Ученик Один':'Ученик Два'}`};
    const created=await create('/core/users',{login:details.login,full_name:details.full_name,password,role:student?'student':'staff_admin',class:student?10:0,...(student?{}:{role_id:details.role_id})});demo[kind]={...details,account_id:created.account_id,profile_id:created.profile_id};await save();
  }
  for(const kind of ['editor','viewer','student','student2']){const entry={path:`/core/divisions/${demo.division_b}/memberships/${demo[kind].account_id}`,id:crypto.randomUUID(),state:'pending'};demo.commands.push(entry);await save();await h.call(entry.path,'PUT',{role_id:kind.startsWith('student')?null:demo[kind].role_id});entry.state='confirmed';await save();}
  demo.outside_exam_id=(await create('/exams',{name:prefix+' outside',exam_type:'outside_lms',direction_id:demo.direction_id,date:'2026-10-09'})).id;
  demo.state='ready';await save();h.pass('Fresh synthetic scopes and canonical roles created through BFF only');return demo;
}
export async function saveFixture(demo){await fs.writeFile(fixturePath,JSON.stringify(demo,null,2),{mode:0o600});}
