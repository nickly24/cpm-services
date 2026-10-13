# Cards XLSX: авторизация фонового применения

Реализация начата после прямого human consent `01a12221-7d06-73f3-8f27-8c05f352977a` в главном чате 9 октября 2026. Прежнее ожидание согласия снято. Это не общий QA RELEASE. Owner adapter реализован в исходниках по [замороженному протоколу Auth revision 2](../../auth/docs/import-operation-grants-v1.md), SHA256 `4aeeccb8497777f1b0fac218bebc52f69dcd162b36683fc3a7756ef5e7bc92bd`. Cards развёрнут в согласованном W2 10 октября в 00:17 МСК: immutable image `727b467afcb622b340f06fdc7378c99e2860da91f91e32f41587b9746ecc3e22`, readiness и Docker health PASS, OpenAPI 1.2.0 / 25 операций совпали с source routes/proof schemas. Из 16 контейнеров изменился только Cards. Использованы ранее выданные credentials; Cards не создавал ключи и не применял DDL. Private proof: `dev/local/cards-operations-runtime-proof.json`. Реальный grant HTTP и UI→Uploads→Rabbit acceptance ещё не выполнен.

## Owner proof — source-stage готов

Native `Session.authorization_proof` — дополнительное поле в parse/edit/GET результата import session:

```json
{
  "scheme": "cards.xlsx.v1",
  "sha256": "<64 lower hex>",
  "source_sha256": "<original XLSX bytes SHA256>",
  "owner_preview_revision": 2,
  "owner_request_sha256": "<SHA256 native RevisionDTO JSON>",
  "target": {"kind":"existing_theme","direction_id":"13","theme_id":"17"}
}
```

Create target: `{kind:"create_theme",direction_id:"13",name:"Имя темы",collision_policy:"reuse_existing"}`; поля theme_id нет. Имя обрезано по краям так же, как при commit. Существующее native поведение сохраняется: тема с таким именем в тех же org/direction переиспользуется. Не вычислять будущий theme ID и не переносить цель в другое направление.

Схема `cards.xlsx.v1`: SHA256 bytes результата Go `encoding/json.Marshal` структуры в фиксированном порядке полей: `scheme,account_id,organization_id,session_id,revision,source_sha256,target,rows`. Account/org/revision — canonical positive decimal strings; session — 32 lowercase hex; source — 64 lowercase hex. Target поля в порядке `kind,direction_id,theme_id?,name?,collision_policy?`. Rows сохраняют порядок preview, поля `row` (integer), `question`/`answer` (точные сохранённые строки UTF-8), `skip` (boolean). Marshal имеет стандартное HTML escaping Go; без завершающего LF. Scheme версия меняется при изменении алгоритма.

Не включены filename/sheet, warnings/summary, текущий state и разрешённый результат name collision: они не меняют подтверждённые строки/цель. Snapshot хранит точные вопросы/ответы до наружного TrimSpace commit, поэтому изменение текста подтверждения всегда меняет proof. Правка revision, creator/org/session, source, direction/theme/name, row index/order/content/skip меняет proof.

Это owner-computed digest; Uploads сохраняет его из native session, не воспроизводит алгоритм и не заменяет собственным wrapper digest. Старые creation receipts могут не содержать additive поле: перед выдачей grant прочитать текущую native session. Transform и legacy preview без проверенного source SHA256 не получают authorization_proof. Пересоздать такой preview из исходного файла через обычный разрешённый flow, не подставлять digest в старую session.

Перед первым apply Cards сравнивает source/proof/revision/target под тем же `SELECT ... FOR UPDATE`, что native commit. Live authorization и matching receipt идут раньше нового domain apply; completed replay не проверяет новые дубли как ещё один импорт. Native input hash квитанции и exact HTTP body SHA256 — отдельные значения.

## Реализованный carrier Auth revision 2

BFF/Auth предложил, Cards принял имена `X-CPM-Operation-Grant`, `X-CPM-Operation-Stage`, `X-CPM-Operation-Job`, `X-CPM-Operation-Service-Key`; stages Cards `preview`, `receipt`, `apply`. Mixed bearer/grant, неизвестный stage, неоднозначные/повторные headers, любой query (включая bare `?`), encoded path и body на GET отвергаются. Только exact session GET, receipt GET закреплённого ключа, session commit POST. Остальные native routes не получают grant доступ.

Обязательное pinned `Idempotency-Key` на всех stages; receipt path должен совпадать с ним. В body commit остаётся только `{expected_revision:N}`. Worker сохраняет однажды сформированные bytes, с них получает wire_body_sha256 и отправляет без повторной сериализации. Пустой GET body имеет SHA256 пустых bytes.

Auth authorize вызывается непосредственно Cards с отдельным owner credential, получает фактический request и возвращает свежий scoped Actor с permissions/organization_admin плюс immutable binding. Cards дополнительно проверяет тип/owner/job/session/nativekey/proofs/target, upload.edit + train.edit и creator/org; организационный scope карточек не заменяется division scope, но original grant division остаётся подтверждённым.

Core dependency ограничена отдельным `core-direction` stage и конкретным direction ID из binding; Core самостоятельно authorizes родительский Cards grant своим удостоверением. Никакого полного cards-context с синтетическим Bearer. Route `GET /internal/core/import-directions/{direction}` и DTO зафиксированы в revision 2. Эта зависимость разрешена и для receipt-only recovery; Cards проверяет account/org/div/job/session/direction/active в ответе.

Env Cards: `AUTH_OPERATIONS_CARDS_KEY` (outbound Auth), `CARDS_OPERATION_DELIVERY_KEY` (incoming Uploads), `CARDS_CORE_OPERATION_KEY` (outbound Core). Все три отдельные и отличны от прежнего AUTH_INTERNAL_KEY; отсутствие всего набора оставляет feature выключенной, частичная конфигурация отклоняется. Core принимает ту же dependency credential как `CORE_CARDS_OPERATION_KEY`. Единственный provision writer — BFF/Auth; deployment только в новом согласованном окне. Существующие credentials не ротируются.

## Границы и проверка

Начальный background тип только `cards.xlsx`. `cards.transform` требует отдельного двух-owner протокола и исключён; его прежний native Bearer flow сохраняется. Прямой Cards UI не получает новый глобальный upload gate.

Нужна адресная приёмка с реальными Auth/Uploads/Rabbit/Core: explicit user confirmation → queued apply → native rows/result; повтор delivery/lost reply без новых keys/rows, current rights/membership/account revoke, expiry, foreign IDs/keys/body/proof, Auth unavailable и recovery-only receipt. UNKNOWN нельзя превращать в failed/no_changes или новый apply. Source proofs уже проверены golden fixture, scope/input mutation cases, collision semantics и legacy/transform rejection; go race/vet passed. Это не end-to-end PASS.

## Source checks и адресный HTTP harness

`go test -race ./...` и `go vet ./...` проверяют strict recursive JSON (включая duplicate/wrong-case/UTF-8/null/target shape), exact HTTP bytes/headers/routes, отдельные credentials Auth/Core, несовпадающие profile/account ID, live permission denial, immutable proof под lock, matched commit receipt и receipt-only recovery. Ошибка или некорректный ответ Auth/Core не допускают обращения к owner DB.

`services/cards/scripts/operation_grants_http_test.py --credentials-file dev/local/import-operation-credentials.env` подготовлен для реального Auth/Core/Cards контура после provisioning и согласованного runtime окна. Он не создаёт ключи и не перезапускает сервисы. Использует собственные role/account/theme/session, проверяет concurrent delivery, exact wire mismatch, правку preview, отзыв train/upload, revoke/recover и single-session logout. Private report перечисляет scope и непокрытые сценарии; этот harness не подтверждает Rabbit/UI flow.

Native commit result сохраняет исходный DTO `{theme_id,card_ids,created,skipped}`. Доказательство завершения — receipt GET по original UUID: `state=completed`, exact `operation=session:commit:<boundSession>`, `request_hash=owner_request_sha256`, matching org/direction/session и original result. Job/key не выдумываются из body; они закреплены carrier/request route и Auth binding. После неизвестного ответа worker читает только этот receipt.

## W4: адресное подтверждение Cards 10 октября

После individual GO координатора выполнено только чтение собственных native записей для Uploads job `711630bd2f1fd775d9b22f88ea64f88f`. Session `f294b19e33bf9247c249653086484e4a` завершена на revision 2 для account 3 / org 1001 / direction 1056. В теме 35 наблюдалась ровно одна карточка 285; её содержимое совпало с подтверждённым CAS preview. Native result: created 1 / skipped 0.

Из журнала Uploads получены original native UUID `2bcae677-492a-541f-a208-b081aa9a3fb1` и owner request hash. По этому UUID вычислена и прочитана подлинная квитанция `b66cda5e9c8cd3dba39ca60a758ec006`: completed, тот же account/org/direction, точная операция commit исходной session, совпадающие request hash и immutable result. Private proof: `dev/local/cards-w4-job-711630bd-proof.json` (0600, без текста карточки/исходных bytes/ключей).

Observer Cards сделал 0 записей и 0 HTTP-запросов; оба read-only подключения закрыты. Это native proof одной успешной операции. Публичное UI-подтверждение, очередь Rabbit и предшествующий нулевой count подтверждаются отдельными отчётами координатора/Uploads. Полный native grant harness не запускался; lost reply, restart, revoke и receipt-only recovery этим результатом не доказаны.

Последняя проверка по отдельному GO: Uploads job `a397f8559c5d66fb50cb0acb83dfc79d` сотрудника account 551 (profile 2125 по Auth/Uploads) также подтверждён native записями. Session `16aecb6d0b895d13fc8f3f2ba04bc814`, revision 1, completed; создана только карточка 286. В теме 35 теперь две разные карточки — 285 и 286. По исходному UUID `50036922-4321-5f78-9a74-a33cae6702e1` найдена квитанция `aae9903119de6f57ed2da025574d1c15` с точными account/org/direction/session/request hash/result. Содержимое карточки совпадает с подтверждённым preview. Private proof: `dev/local/cards-w4-job-a397f855-proof.json`. Одна read-only транзакция, 0 записей/HTTP; соединение закрыто. Проверки видимости UI и фактических прав сотрудника остаются в отчётах координатора/Auth/Uploads. Работа observer Cards завершена; дополнительных probes/holds нет.
