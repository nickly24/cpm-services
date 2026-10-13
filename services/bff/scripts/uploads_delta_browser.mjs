/** Actual common Uploads forms/templates/edits. No domain apply: catalog commit:false stays authoritative. */
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import crypto from 'node:crypto';
import { harness, fixture, saveFixture, workbook, workbookRows, password, output, independent } from './uploads_delta_helpers.mjs';
const resumeRemaining=process.argv.includes('--resume-remaining'), readOnly=process.argv.includes('--read-only'),correctUnsafe=process.argv.includes('--correct-confirmed-unsafe-fixture'),readOnlyTail=process.argv.includes('--read-only-tail');
assert(!readOnlyTail||resumeRemaining&&correctUnsafe,'Readonly tail must reuse the explicitly amended, fully confirmed fixture');
assert(!correctUnsafe||resumeRemaining,'Corrected unsafe preview is a separate explicit remaining-only intent');
const plan={run:'6078af3e',prefix:'DEV Joint 6078af3e',organizationId:1001,divisionId:10249,otherDivisionId:10250,actorId:536,maxSessions:correctUnsafe?6:5,maxEdits:2,endsAt:'2026-10-09T22:18:00+03:00'};
let retainedFixture;
assert(!process.argv.includes('--plan')||resumeRemaining,'Plan mode never launches the generic harness');
if(resumeRemaining){
  assert(independent&&!readOnly,'Remaining acceptance requires its existing independent manifest, not generic/read-only setup');
  const retained=JSON.parse(await fs.readFile(output+'bff-uploads-delta-independent-demo.json','utf8'));
  retainedFixture=retained;
  assert.equal(retained.run,plan.run);assert.equal(retained.prefix,plan.prefix);assert.equal(retained.organization_id,plan.organizationId);assert.equal(retained.division_a,plan.divisionId);assert.equal(retained.division_b,plan.otherDivisionId);assert.equal(retained.editor.account_id,plan.actorId);
  assert.equal(retained.state,'ready');assert(!retained.pending_upload&&!retained.pending_edit&&!retained.pending_external,'Resolve the original pending command by canonical reads before continuing');
  assert(retained.external_id&&retained.outside_exam_id,'No new owner targets may be created during remaining acceptance');
  if(correctUnsafe){assert.equal(retained.remaining_uploads?.['unsafe-ids'],'1d91370261e0b99514e3903818cceeba');assert(retained.confirmed_upload_commands.some(row=>row.command_id==='69a582f3-64c6-4910-99f0-b676a342f224'&&row.session_id===retained.remaining_uploads['unsafe-ids']));}
  if(process.argv.includes('--plan')){console.log(JSON.stringify({mode:'source_only_plan',...plan,retainedPreviews:retained.confirmed_upload_commands.length,cases:['tests-json','tests-otp','external-results',correctUnsafe?'unsafe-ids-corrected':'unsafe-ids','outside-exam'],confirmedRemaining:retained.remaining_uploads??{},nativePreviewMax:plan.maxSessions,ownerSourceWrites:0,applyCalls:0,runRequires:'Root READY3 and isolated window ACK'},null,2));process.exit(0);}
  assert(Date.now()<Date.parse(plan.endsAt),'RUN cannot start after the authorized window; no automatic extension');
  assert.equal(process.env.CPM_UPLOADS_RUN_WINDOW,correctUnsafe?'296-ui535-6078af3e-six':'296-ui535-6078af3e','RUN requires root READY3 plus the isolated window and extra-preview ACK');
}
const writePolicy=resumeRemaining?{...plan,alreadyCreated:retainedFixture.confirmed_upload_commands.filter(row=>row.case).length,alreadyEdited:retainedFixture.confirmed_edit_commands?.filter(row=>row.case).length??0,editableSessions:new Set()}:undefined;
const label=resumeRemaining?'uploads-delta-independent-remaining':independent?'uploads-delta-independent':'uploads-delta';
const h=await harness(label,writePolicy);let demo,status='failed',activeCase;
const labels={'users.credentials':'Пользователи с готовыми учётными данными','users.legacy':'Пользователи: школы и прокторы','cards.xlsx':'Карточки из Excel','tests.json':'Тесты из JSON / OnlineTestPad','tests.external-results':'Результаты внешних тестов','exams.outside':'Результаты экзамена вне LMS'};
const response=(path,method='GET')=>h.page.waitForResponse(res=>new URL(res.url()).pathname===`/bff${path}`&&res.request().method()===method);
async function open(role='admin'){await h.open('upload',role);await h.page.getByRole('heading',{name:'Загрузка',exact:true}).waitFor();await h.page.getByText('Загружаем доступные форматы и журнал',{exact:true}).waitFor({state:'hidden'});}
async function select(type){await open(h.identity.user.role);await h.page.getByRole('button').filter({has:h.page.getByText(labels[type],{exact:true})}).click();await h.page.getByRole('heading',{name:labels[type],exact:true}).waitFor();}
async function guard(){const sibling=await h.context.newPage();await sibling.goto(`http://127.0.0.1:3001/cabinet/${h.identity.user.role}/upload`);const select=sibling.getByLabel('Переключить кабинет и подразделение');await select.waitFor();const old=await select.inputValue();await select.selectOption(`${demo.organization_id}:${demo.division_b}`);await sibling.getByText('Закройте редактор',{exact:false}).waitFor();assert.equal(await select.inputValue(),old);await sibling.close();}
async function upload(type,filename,buffer,setup,drop=false){
  await select(type);if(setup)await setup();await h.page.getByLabel('Файл',{exact:true}).setInputFiles({name:filename,mimeType:filename.endsWith('.xlsx')?'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet':'text/plain',buffer});
  demo.uploads??={};demo.pending_upload={type,state:'not_dispatched',...(activeCase?{case:activeCase,filename,source_sha256:crypto.createHash('sha256').update(buffer).digest('hex')}:{})};await saveFixture(demo);
  let dropped=false,resolveDrop,rejectDrop;const droppedReply=new Promise((resolve,reject)=>{resolveDrop=resolve;rejectDrop=reject;});
  const before=h.requests.filter(r=>r.path==='/bff/uploads/sessions'&&r.method==='POST').length;
  await h.page.route('**/bff/uploads/sessions',async route=>{if(route.request().method()!=='POST')return route.fallback();demo.pending_upload.command_id=route.request().headers()['idempotency-key'];assert(demo.pending_upload.command_id);await saveFixture(demo);if(!drop||dropped)return route.fallback();dropped=true;try{assert((await route.fetch()).ok());await route.abort('failed');resolveDrop();}catch(error){rejectDrop(new Error(`Dropped response did not finish: ${error.name}`));}});
  const res=drop?null:response('/uploads/sessions','POST');demo.pending_upload.state='dispatched';await saveFixture(demo);
  await h.page.getByRole('button',{name:'Подготовить предпросмотр',exact:true}).click();
  if(res)assert.equal((await res).status(),201);else{await Promise.race([droppedReply,new Promise((_,reject)=>setTimeout(()=>reject(new Error('Owner response callback did not finish')),30000))]);const recover=h.page.getByRole('button',{name:'Проверить исход',exact:true});await recover.waitFor();await h.page.waitForFunction(()=>[...document.querySelectorAll('button')].some(button=>button.textContent==='Проверить исход'&&!button.disabled));assert(dropped);assert.equal(await h.page.evaluate(value=>Object.values(sessionStorage).join().includes(value),password),false);await h.page.unroute('**/bff/uploads/sessions');await recover.click();}
  await h.page.unroute('**/bff/uploads/sessions');await h.page.getByRole('heading',{name:'Предпросмотр загрузки',exact:true}).waitFor();const receipt=await h.call(`/uploads/receipts/${demo.pending_upload.command_id}`);assert.equal(receipt.type,type);assert.equal(receipt.command_id,demo.pending_upload.command_id);assert.equal(receipt.state,'completed');const current=await h.call(`/uploads/sessions/${receipt.resource_id}`);assert.equal(current.type,type);demo.uploads[type]=current.id;demo.confirmed_upload_commands??=[];demo.confirmed_upload_commands.push({command_id:demo.pending_upload.command_id,session_id:current.id,type,...(activeCase?{case:activeCase,source_sha256:demo.pending_upload.source_sha256}:{})});if(activeCase){demo.remaining_uploads??={};demo.remaining_uploads[activeCase]=current.id;}delete demo.pending_upload;await saveFixture(demo);
  assert.equal(h.requests.filter(r=>r.path==='/bff/uploads/sessions'&&r.method==='POST').length,before+1);assert.equal(await h.page.getByRole('button',{name:'Подтвердить импорт',exact:true}).isDisabled(),true);await h.page.getByText('Предпросмотр доступен. Фоновое применение ожидает разрешения доступа',{exact:false}).waitFor();return current;
}
async function save(sessionId){
  if(writePolicy)writePolicy.editableSessions.add(sessionId);
  const path=`/uploads/sessions/${sessionId}`, before=await h.call(path);
  await h.page.route(`**/bff${path}`,async route=>{
    if(route.request().method()!=='PUT')return route.fallback();
    const key=route.request().headers()['idempotency-key'];assert(key);
    demo.pending_edit={case:activeCase,session_id:sessionId,command_id:key,method:'PUT',path,request_sha256:crypto.createHash('sha256').update(route.request().postData()??'').digest('hex'),body:route.request().postDataJSON(),state:'dispatched'};await saveFixture(demo);return route.fallback();
  });
  const res=response(path,'PUT');await h.page.getByRole('button',{name:'Сохранить правки',exact:true}).click();assert.equal((await res).status(),200);await h.page.unroute(`**/bff${path}`);await h.page.getByText('Есть несохранённые правки',{exact:false}).waitFor({state:'hidden'});
  const current=await h.call(path);assert(current.revision>before.revision,'Coordinator CAS revision must advance');
  demo.confirmed_edit_commands??=[];demo.confirmed_edit_commands.push({...demo.pending_edit,state:'confirmed',revision:current.revision});delete demo.pending_edit;await saveFixture(demo);return current;
}
async function retainedSession(id){
  await open(h.identity.user.role);await h.page.getByRole('button',{name:'Журнал',exact:true}).click();
  const sessions=await h.call('/uploads/sessions?limit=50'),index=sessions.findIndex(session=>session.id===id);assert(index>=0,'Confirmed preview must still exist in this actor scope; no automatic recreation');
  await h.page.getByRole('button',{name:'Предпросмотр',exact:true}).nth(index).click();await h.page.getByRole('heading',{name:'Предпросмотр загрузки',exact:true}).waitFor();return h.call(`/uploads/sessions/${id}`);
}
async function remaining(caseName,create,verify){
  activeCase=resumeRemaining?caseName:undefined;
  const id=resumeRemaining?demo.remaining_uploads?.[caseName]:undefined;
  const current=id?await retainedSession(id):await create();
  await verify(current,!!id);activeCase=undefined;return current;
}
async function download(expected,target,format='cpm_json'){const read=response('/uploads/templates/'+expected);const event=h.page.waitForEvent('download');await h.page.getByRole('button',{name:'Скачать шаблон',exact:true}).click();const res=await read;assert.equal(res.status(),200);const value=await event;const json=expected==='tests.json',extension=json?(format==='online_test_pad'?'.js':'.json'):expected==='users.credentials'?'.csv':'.xlsx';const mime=json?(format==='online_test_pad'?'text/javascript':'application/json'):expected==='users.credentials'?'text/csv':'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet';assert(value.suggestedFilename().endsWith(extension));assert.equal(res.headers()['content-type'].split(';')[0],mime);const query=new URL(res.url()).searchParams;if(target)assert.equal(query.get('target_id'),String(target));if(json){assert.equal(query.get('direction_id'),String(demo.direction_id));assert.equal(query.get('format'),format);}assert(!query.has('organization_id')&&!query.has('division_id'));return fs.readFile(await value.path());}
try{
  demo=await fixture(h);
  if(readOnlyTail){
    const prior=JSON.parse(await fs.readFile(output+'bff-uploads-delta-independent-remaining-subjects-confirmed-report.json','utf8'));assert.equal(prior.check_count,19);assert.equal(prior.fixture.run,demo.run);assert.deepEqual(prior.cleanupErrors,[]);assert(!demo.pending_upload&&!demo.pending_edit);assert.equal(demo.confirmed_upload_commands.filter(row=>row.case).length,6);assert.equal(demo.confirmed_edit_commands.filter(row=>row.case).length,2);h.checks.push(...prior.checks);
  }else{
  await h.login('dev_admin',demo.division_a);
  if(independent&&!demo.external_id){assert(!readOnly,'Readonly requires a confirmed independent external target');assert(!demo.pending_external,'Unknown external target create must be recovered with its original UUID');demo.pending_external={command_id:crypto.randomUUID(),state:'dispatched'};await saveFixture(demo);const external=await h.call('/tests/external-tests','POST',{name:demo.prefix+' upload target',direction_id:demo.direction_id,date:'2026-10-09'},201,demo.pending_external.command_id);demo.external_id=external.id;delete demo.pending_external;await saveFixture(demo);}
  assert(demo.external_id,'External Tests browser fixture must be ready first');
  if(independent)await h.login(demo.editor.login,demo.division_a);await open(h.identity.user.role);
  const catalog=await h.call('/uploads/catalog');for(const type of catalog.types.filter(t=>t.create))assert.equal(type.commit,false);h.pass('Live owner advertises prepared formats but no background apply capability');
  await select('users.credentials');await download('users.credentials');h.pass('Protected credential template is a real CSV without generated credentials');
  if(!readOnly){
    assert(!demo.pending_upload,'Recover previous unknown upload in UI before running a new form');
    if(resumeRemaining){
      assert.equal(demo.confirmed_upload_commands.filter(row=>['users.credentials','users.legacy','cards.xlsx'].includes(row.type)).length,3);
      for(const type of ['users.credentials','users.legacy','cards.xlsx']){const current=await h.call(`/uploads/sessions/${demo.uploads[type]}`);assert.equal(current.type,type);assert.equal(current.actor_account_id,plan.actorId);assert.equal(current.organization_id,plan.organizationId);assert.equal(current.division_id,plan.divisionId);}
      if(correctUnsafe){const original=await h.call('/uploads/sessions/1d91370261e0b99514e3903818cceeba'),metadata=(await h.call(`/uploads/files/${original.file_id}`)).file;assert(/^[a-f0-9]{64}$/.test(metadata.sha256));assert.equal(original.source_hash,metadata.sha256);const binding={session_id:original.id,command_id:'69a582f3-64c6-4910-99f0-b676a342f224',file_id:original.file_id,source_sha256:metadata.sha256,reason:'Known confirmed XLSX cell index put the huge text into login; retained without edit or replay'};if(demo.known_misaligned_unsafe_preview)assert.deepEqual(demo.known_misaligned_unsafe_preview,binding);else{demo.known_misaligned_unsafe_preview=binding;await saveFixture(demo);}}
      h.pass('Remaining acceptance retains the first three confirmed previews and all seven prior checks without repeating creation, legacy edits or Cards lost reply');
    }else{
    const credentials=await upload('users.credentials','users.csv',Buffer.from(`full_name,login,password,class\n${demo.prefix} Учётный,preview_${demo.run},${password},10\n`));assert(!JSON.stringify(credentials).includes(password));assert.equal(await h.page.locator('textarea').count(),0);h.pass('Fresh credential preview is masked and actor-private; previous reserved command is left untouched');await guard();h.pass('Preview holds header context across tabs');
    await select('users.legacy');const legacyHeaders=workbookRows(await download('users.legacy'))[0];assert.deepEqual(legacyHeaders,['ФИО','Класс','Школа','Проктор','Telegram']);const legacy=await upload('users.legacy','legacy.xlsx',workbook([legacyHeaders,[`Импорт${demo.run} Новый`,10,'','','@synthetic_new'],[demo.student.full_name,10,'','','']]));await h.page.getByLabel('Класс из файла, строка 2',{exact:true}).fill('11');const updated=await save(legacy.id);assert.equal(updated.preview.preview.students[0].class_raw,'11');assert.equal(updated.preview.preview.students[0].tg_name,'@synthetic_new');assert.equal(updated.preview.preview.students.length,2);await h.page.getByText('требуют сброса пароля',{exact:false}).first().waitFor();h.pass('Native five-column legacy template feeds editable full raw snapshot with owner reset-required policy and no apply');
    const cards=await upload('cards.xlsx','cards.xlsx',workbook([['Вопрос','Ответ'],[`${demo.prefix} Вопрос`,'Строка 1\nСтрока 2']]),async()=>{await h.page.getByLabel(/^Направление/).selectOption(String(demo.direction_id));await h.page.getByLabel('Название темы',{exact:true}).fill(`${demo.prefix} Cards ${Date.now().toString(16)}`);},true);await h.page.getByLabel('Ответ, строка 2',{exact:true}).fill('Исправленный\nполный ответ');const savedCards=await save(cards.id);assert.equal(savedCards.preview.preview.cards[0].answer,'Исправленный\nполный ответ');h.pass('Cards lost create reply recovers its original receipt with one POST and preserves multiline CAS edits');
    }
    await remaining('tests-json',async()=>{
      await select('tests.json');await h.page.getByLabel(/^Направление/).selectOption(String(demo.direction_id));await h.page.getByLabel(/^Формат шаблона/).selectOption('cpm_json');
      const jsonSource=await download('tests.json');return upload('tests.json','test.json',jsonSource,async()=>h.page.getByLabel(/^Направление/).selectOption(String(demo.direction_id)));
    },async current=>{
      assert.equal(current.preview.preview.valid,true);assert.equal(current.preview.preview.preview.questions.length,3);assert.equal(await h.page.locator('textarea').count(),0);
      h.pass('Real CPM JSON template round-trips into native questions, keys and summary without second validation or execution');
    });
    await remaining('tests-otp',async()=>{
      await select('tests.json');await h.page.getByLabel(/^Направление/).selectOption(String(demo.direction_id));await h.page.getByLabel(/^Формат шаблона/).selectOption('online_test_pad');
      const otpSource=await download('tests.json',undefined,'online_test_pad');return upload('tests.json','export.js',Buffer.concat([otpSource,Buffer.from('\nwindow.__UPLOAD_MUST_NOT_EXECUTE__ = true;')]),async()=>h.page.getByLabel(/^Направление/).selectOption(String(demo.direction_id)));
    },async current=>{
      assert.equal(current.preview.preview.source,'online_test_pad');assert.equal(await h.page.evaluate(()=>window.__UPLOAD_MUST_NOT_EXECUTE__),undefined);
      h.pass('Real OnlineTestPad JS template is parsed by owners and never executed in the browser');
    });
    let externalHeaders;
    const externalSetup=async()=>h.page.getByLabel(/^Внешний тест/).selectOption(demo.external_id);
    await remaining('external-results',async()=>{
      await select('tests.external-results');await externalSetup();externalHeaders=workbookRows(await download('tests.external-results',demo.external_id))[0];
      assert.deepEqual(externalHeaders,['ФИО','Процент правильных ответов (%)','Количество правильных ответов','Дата завершения','Потрачено времени','Логин','account_id','legacy_student_id']);
      return upload('tests.external-results','external.xlsx',workbook([externalHeaders,[demo.student.full_name,0,99,'','',demo.student.login,String(demo.student.account_id),''],[demo.student2.full_name,'',1,'','',demo.student2.login,String(demo.student2.account_id),'']]),externalSetup);
    },async current=>{
      assert.equal(current.target_id,demo.external_id);assert.equal(current.preview.preview.rows[0].percent,0);assert.equal(current.preview.preview.rows[0].account_id,demo.student.account_id);
      const edited=demo.confirmed_edit_commands?.some(row=>row.case==='external-results'&&row.session_id===current.id);
      if(!edited){assert.equal(current.preview.preview.rows[1].percent,null);assert.equal(await h.page.getByLabel('Процент из файла, строка 3',{exact:true}).inputValue(),'');await h.page.getByLabel('Процент из файла, строка 3',{exact:true}).fill('25.5');current=await save(current.id);}
      assert.equal(current.preview.preview.rows[1].percent,25.5);assert.equal(current.preview.preview.rows[0].percent,0);assert.equal(current.preview.preview.rows[0].correct_count,99);
      h.pass('Real eight-column external XLSX preserves scoped text account IDs, true zero and explicit null, then structured CAS changes only the requested percentage');
    });
    await remaining(correctUnsafe?'unsafe-ids-corrected':'unsafe-ids',async()=>{
      await select('tests.external-results');await externalSetup();externalHeaders??=workbookRows(await download('tests.external-results',demo.external_id))[0];
      const current=await upload('tests.external-results',correctUnsafe?'unsafe-account-id-corrected.xlsx':'unsafe-ids.xlsx',workbook([externalHeaders,[demo.student.full_name,0,1,'','','','9007199254740993','']]),externalSetup);if(correctUnsafe)demo.unsafe_external_upload_corrected=current.id;else demo.unsafe_external_upload=current.id;await saveFixture(demo);return current;
    },async()=>{
      if(correctUnsafe){assert.notEqual(demo.unsafe_external_upload_corrected,demo.known_misaligned_unsafe_preview.session_id);const corrected=await h.call(`/uploads/sessions/${demo.unsafe_external_upload_corrected}`);assert.notEqual(corrected.source_hash,demo.known_misaligned_unsafe_preview.source_sha256);}
      await h.page.getByText('ID вне поддерживаемого диапазона редактирования; используйте исходный XLSX с текстовыми IDs',{exact:true}).waitFor();
      await h.page.getByText('ID не отображается: точное значение недоступно',{exact:true}).first().waitFor();assert.equal(await h.page.locator('textarea').count(),0);assert.equal(await h.page.getByText('9007199254740992',{exact:true}).count(),0);assert.equal(await h.page.getByRole('button',{name:'Сохранить правки',exact:true}).count(),0);
      await h.mobile('unsafe-preview');h.pass('Native unsafe IDs remain read-only without a fabricated exact number or rounded structured edit');
    });
    await remaining('outside-exam',async()=>{
      await select('exams.outside');await h.page.getByLabel(/^Экзамен/).selectOption(String(demo.outside_exam_id));const headers=workbookRows(await download('exams.outside',demo.outside_exam_id))[0];
      assert.deepEqual(headers,['student_id','student_login','points','grade','examinator']);
      return upload('exams.outside','outside.xlsx',workbook([headers,[demo.student.account_id,demo.student.login,0,0,'Синтетический Экзаменатор']]),async()=>h.page.getByLabel(/^Экзамен/).selectOption(String(demo.outside_exam_id)));
    },async current=>{
      assert.equal(Number(current.target_id),demo.outside_exam_id);assert.equal(current.preview.rows[0].input.points,0);assert.equal(current.preview.rows[0].input.grade,0);
      const edited=demo.confirmed_edit_commands?.some(row=>row.case==='outside-exam'&&row.session_id===current.id);
      if(!edited){await h.page.getByLabel('Баллы, строка 2',{exact:true}).fill('0');await h.page.getByLabel('Оценка, строка 2',{exact:true}).fill('0');await h.page.getByLabel('Экзаменатор, строка 2',{exact:true}).fill(`Синтетический Экзаменатор ${demo.run} проверено`);current=await save(current.id);}
      assert.equal(current.preview.rows[0].input.points,0);assert.equal(current.preview.rows[0].input.grade,0);
      h.pass('Real protected outside-exam template and structured CAS preserve genuine zero points and grade with the fixed own target');
    });
  }else{assert(Object.keys(demo.uploads??{}).length>=6);h.pass('Readonly repeat reuses confirmed native previews without importing or editing');}
  await select('cards.xlsx');await download('cards.xlsx');h.pass('Protected Cards template is actual XLSX');await select('exams.outside');assert.equal(await h.page.getByRole('button',{name:'Скачать шаблон',exact:true}).isDisabled(),true);await h.page.getByLabel(/^Экзамен/).selectOption(String(demo.outside_exam_id));await download('exams.outside',demo.outside_exam_id);h.pass('Protected Exams template requires and retains selected target ID');
  await select('users.legacy');const legacyTemplate=await download('users.legacy');assert.deepEqual(workbookRows(legacyTemplate)[0],['ФИО','Класс','Школа','Проктор','Telegram']);h.pass('Protected Core legacy template preserves all five native columns');
  await select('tests.json');assert.equal(await h.page.getByRole('button',{name:'Скачать шаблон',exact:true}).isDisabled(),true);await h.page.getByLabel(/^Направление/).selectOption(String(demo.direction_id));await h.page.getByLabel(/^Формат шаблона/).selectOption('cpm_json');const nativeJson=await download('tests.json');JSON.parse(nativeJson.toString('utf8'));await h.page.getByLabel(/^Формат шаблона/).selectOption('online_test_pad');const nativeJs=await download('tests.json',undefined,'online_test_pad');assert(nativeJs.length>0);assert.equal(await h.page.evaluate(()=>window.__UPLOAD_MUST_NOT_EXECUTE__),undefined);h.pass('Protected Tests templates retain selected direction and CPM JSON or raw OTP JS MIME without execution');
  await select('tests.external-results');assert.equal(await h.page.getByRole('button',{name:'Скачать шаблон',exact:true}).isDisabled(),true);await h.page.getByLabel(/^Внешний тест/).selectOption(demo.external_id);const externalTemplate=await download('tests.external-results',demo.external_id);assert(workbookRows(externalTemplate)[0].length>0);h.pass('Protected external-result template is XLSX pinned to the selected scoped target');
  const noTemplate=catalog.types.find(type=>type.create&&type.template!==true&&labels[type.type]);if(noTemplate){await select(noTemplate.type);const before=h.requests.filter(request=>request.path.startsWith('/bff/uploads/templates/')).length;assert.equal(await h.page.getByRole('button',{name:'Скачать шаблон',exact:true}).isDisabled(),true);assert.equal(h.requests.filter(request=>request.path.startsWith('/bff/uploads/templates/')).length,before);h.pass('Unavailable template capability stays disabled before any request');}
  await h.page.getByRole('button',{name:'Журнал',exact:true}).click();assert.equal(await h.page.getByText('Импорт подтверждён',{exact:true}).count(),0);assert.equal(await h.page.getByRole('button',{name:'Повторить доставку задания',exact:true}).count(),0);await h.mobile('journal');h.pass('Journal never fabricates completion or retries while execution lacks authorization');
  await h.cabinet(demo.division_b);assert.equal((await h.call('/uploads/sessions')).length,0);const foreignSession=Object.values(demo.uploads)[0];await h.call(`/uploads/sessions/${foreignSession}`,'GET',undefined,404);h.pass('Header division switch hides original-scoped sessions and prevents cross-cabinet reads');
  await h.login(demo.viewer.login,demo.division_a);await open('staff_admin');const viewer=await h.call('/uploads/catalog');assert(viewer.types.every(t=>!t.create&&!t.edit&&!t.commit));assert.equal(await h.page.getByRole('button',{name:'Подготовить предпросмотр',exact:true}).isDisabled(),true);assert.equal((await h.call('/uploads/sessions')).length,0);await h.call(`/uploads/sessions/${foreignSession}`,'GET',undefined,404);h.pass('Readonly staff sees allowed formats and only actor-private journals without editing');
  await h.login(independent?'dev_admin':demo.editor.login,demo.division_a);await open(h.identity.user.role);const editor=await h.call('/uploads/catalog');assert(editor.types.some(t=>t.type==='tests.external-results'&&t.create&&t.edit&&!t.commit));assert.equal((await h.call('/uploads/sessions')).length,0);h.pass('Other authorized actor has subject-bound preparation rights and cannot see the import author journal');
  }
  const privateSession=Object.values(demo.uploads)[0];
  await h.login(demo.student.login,demo.division_a);const studentCatalog=await h.call('/uploads/catalog');
  assert(studentCatalog.types.every(type=>['homework.pdf','attendance.zap-images'].includes(type.type)&&type.create===false&&type.edit===false&&type.commit===false&&type.template===false));
  await h.call('/uploads/sessions','GET',undefined,403);await h.call('/uploads/jobs','GET',undefined,403);await h.call(`/uploads/sessions/${privateSession}`,'GET',undefined,404);
  await h.open('upload','student');await h.page.getByRole('heading',{name:'404',exact:true}).waitFor();assert.equal(await h.page.getByRole('button',{name:'Подготовить предпросмотр',exact:true}).count(),0);assert.equal(await h.page.getByRole('link',{name:'Загрузка',exact:true}).count(),0);
  h.pass('Student catalog exposes only own-file links; administrative journals return403, private actor session404 and administrative upload UI returns404',{catalog_types:studentCatalog.types.map(type=>type.type)});
  assert.equal(h.requests.filter(r=>/\/uploads\/(sessions\/[^/]+\/commit|jobs\/[^/]+\/retry)$/.test(r.path)).length,0);assert.deepEqual(h.errors,[]);assert.deepEqual(h.remote,[]);assert.deepEqual(h.legacy,[]);assert.deepEqual(h.blockedWrites,[]);if(writePolicy){assert(h.businessWrites.filter(row=>row.method==='POST').length<=plan.maxSessions);assert(h.businessWrites.filter(row=>row.method==='PUT').length<=plan.maxEdits);assert(h.businessWrites.every(row=>row.key!=='44083500-b582-4c23-bd1c-67c2e9e367a7'));}h.pass('No apply/retry business calls, remote legacy fallback, secret journal or browser JS errors');status='passed';
}catch(error){await h.page?.screenshot({path:output+`bff-${label}-failure.png`,fullPage:true});console.error(`${error.name}: ${String(error.message).split('Call log:')[0].trim()}`);process.exitCode=1;}finally{await h.finish(status,{...demo,readOnly,independent,resumeRemaining,correctUnsafe,readOnlyTail,...(resumeRemaining?{write_budget:plan,cumulative_new_previews:demo?.confirmed_upload_commands?.filter(row=>row.case).length??0,cumulative_new_edits:demo?.confirmed_edit_commands?.filter(row=>row.case).length??0,known_misaligned_report:correctUnsafe?'bff-uploads-delta-independent-remaining-known-misaligned-report.json':undefined,subjects_confirmed_checkpoint:readOnlyTail?'bff-uploads-delta-independent-remaining-subjects-confirmed-report.json':undefined}:{}),previous_reserved_command_tested:false});}
