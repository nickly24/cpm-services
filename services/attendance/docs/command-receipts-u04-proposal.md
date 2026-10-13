# U04: восстановление результата команды после reload

Статус: exact native contract принят владельцем BFF после чтения этого файла и подтверждён координатором. Native source заморожен: девять групп целевых тестов, реальные изолированные MySQL fixtures, Go race/vet прошли; source OpenAPI содержит39 операций. BFF/UI implementation — только владелец BFF. Native dev runtime обновлён21:34:04МСК; OpenAPI39/health подтверждены. Targeted native HTTP baseline прошёл5 сценариев/925 assertions,11 собственных новых UUID-команд, Core writes0, logout/cleanup без ошибок. BFF296 обновлён21:44:15МСК, UI535/57 optimized готов21:37:04МСК. Actual U04 browser прошёл15 сценариев; роли/Auth/context/heldGET закрыты, ошибки0. Attendance→BFF→UI U04 завершён. Отдельная scan-приёмка 09.10.2026 прошла четыре сценария и не доказывает восстановление неизвестной команды после reload.

## Подтверждённый пробел

До исправления `components/attendance/shared.tsx` хранил Command и callback только в React. Перезагрузка или уход со страницы теряли UUID и исходные bytes. Кроме того, повтор после неизвестного исхода удалял pending при любом ответе <500, в том числе401/403: потеря текущего доступа не доказывает отсутствие первоначального commit. Прежний browser lost-reply proof повторял команду в том же открытом диалоге.

Native receipt уже привязан к `(organization_id, actor_account_id, request_key)`. `response` содержит исходные revision/value, а audit этой revision — actor, исходный method/path и immutable snapshot. State/audit/receipt записываются одной транзакцией. Успешные receipts сейчас не имеют TTL или GC. Тело и QR не нужны для положительного подтверждения commit.

Отсутствующая receipt-строка не доказывает rollback: первый запрос может ещё проходить Auth/Core и войти в транзакцию позже. Ни GET, ни ожидание блокировки state сами по себе не создают доказательство terminal absence. Поэтому новый контракт не обещает `not_applied`, cancellation или expiration.

## Один public GET для всех видов команд

```http
GET /v1/attendance/command-receipts/{canonical_uuid}
GET /bff/attendance/command-receipts/{canonical_uuid}
```

Только canonical UUID, без query, request body, списка receipts, actor/org overrides или service-key обхода. Current actor и выбранная организация берутся из live Auth. BFF сохраняет обычные cookie/context проверки. Ответ и ошибки `no-store`, `nosniff`.

```json
{
  "status": true,
  "data": {
    "revision": 251,
    "value": {
      "contract_version": 1,
      "command_id": "fb802334-eedb-4b1b-9668-91bb6052b6cd",
      "state": "committed",
      "organization_id": 1001,
      "scope": "division",
      "division_ids": [10150],
      "method": "POST",
      "path": "/divisions/10150/class-days/141/attendance"
    }
  }
}
```

`revision` — исходная committed revision. `path` — исходный command-relative path, без `/v1/attendance`; `method/path` должны совпасть с сохранённой UI metadata. `scope`: `division`, `divisions` для bulk либо `organization` для rooms. Массив division IDs отсортирован, уникален, не более100 элементов; organization scope использует пустой массив. Наружу не возвращаются original Value, body, expected_revision, hash, QR, изображения, полное имя или audit snapshot. После200 UI перечитывает актуальные данные; исторический Result не превращается в текущий state.

| Ответ | Что он доказывает | UI |
|---|---|---|
| 200 committed | Exact UUID/actor/org command завершился; original scope заново авторизован | Сверить metadata, очистить только совпавшую запись, refresh |
| 400 invalid parameters | GET/metadata несовместимы с контрактом | Сохранить UUID, показать диагностику |
| 401 invalid token | Нужна новая авторизация | Сохранить UUID; reconciliation после входа тем же actor/org |
| 403 forbidden | Текущие права исходной операции отозваны | Сохранить UUID; это не rollback |
| 404 not found | Scoped row или доступный scope не найден | Сохранить UNKNOWN; никакого нового UUID/авторетрай |
| 409 receipt_scope_unverifiable | Старый bulk не содержит доказуемого полного scope | Сохранить UNKNOWN; без догадок по diff/counts |
| 503 dependency unavailable | Auth/Core/DB/audit/scope evidence недоступны или повреждены | Сохранить UNKNOWN, повторить только GET |

Missing own row и guessed foreign actor/org UUID дают одинаковый404 без lookup за пределами текущего actor/org. Не существует `410 expired`, пока нет реальной политики retention с tombstone. GET никогда не вызывает Mutate, history guard или повтор исходной команды.

## Авторизация и серверные scope metadata

Перед200 проверяются audit actor/revision/action и допустимый исходный write route; текущие права именно этой операции, все исходные подразделения и их активность в Core. Выбранный кабинет не подменяет explicit scope — как в текущем Attendance. Archive/delete исходного предметного объекта или типа не требуют его текущего существования/активности. Архив подразделения остаётся недоступным.

Для mark/scan/unmark проктора повторяются текущий target/group/назначение и immutable original group. Original Result/audit используются внутри сервера. Успешный scan receipt читается после QR expiry/rotation без повторной проверки QR. Student zap-create дополнительно связан с собственным account. Rooms требует текущего full admin; bulk — текущие schedule view/edit по всем исходным division IDs.

Полный bulk scope нельзя вывести из нынешнего `created_ids/updated_count/deleted_count` и post-snapshot: удалённые targets исчезли, noop update не виден в diff. Для **новых** receipts вводится private `scope_metadata` внутри существующего SQL `response` JSON. Public first response/replay сохраняют прежний Result `{revision,value}`: private metadata никогда не выдаётся через обычные writes. Новая колонка/таблица/DDL не нужны.

Private metadata содержит version, original method/path/kind, scope и все validated original division IDs. Она создаётся после успешного write fn из typed input и записывается в той же транзакции с state/audit/receipt. Для bulk scope — union division IDs всех validated creates/updates/deletes, включая noop updates; не diff, counts или текущий browser выбор. Прочие division routes берут division из серверного route. Rooms имеет organization scope.

Старые division-path receipts можно читать по точному audit action и immutable state/result. Старые rooms допускаются только через известный original room route + current admin/org. Старые bulk без metadata возвращают409. Никакого скрытого backfill или переписывания существующей истории.

## UI и порядок реализации

До отправки UI сохраняет в localStorage только UUID/account namespace/org/original selected division/method/path/original revision. Это согласованный выбор владельца UI для cross-tab/relogin. QR/body/credentials/Value/callback не сохраняются. Записи разделены по текущему actor/org/original selected division и bounded по числу/размеру; превышение лимита или storage failure блокируют новую команду до отправки, не удаляя неизвестный исход.

Пока исходное тело есть в RAM, допустим прежний exact retry. После reload — только GET receipt и refresh. 401/403/404/409/503 сохраняют unknown metadata. Неизвестная запись блокирует потенциально пересекающуюся новую mutation; известная ошибка исходной первой попытки и ошибка retry неизвестной команды обрабатываются раздельно. Восстановленная200 metadata должна совпасть с UUID/org/method/path; mismatch не очищает запись. Автоматическое создание нового UUID или восстановление QR/body запрещены.

После exact ACK: Native source/meaningful Go+SQL tests → новый DTO/OpenAPI → согласованная Attendance-only сборка/runtime → targeted actual HTTP → BFF/UI owner implementation → actual lost-reply/reload/live-revoke/restore/browser proof. Native Go writer один; другой агент только готовит отдельный HTTP harness до нового execution window. Не пересекать согласованные Core/BFF restart windows; никаких shared roles/settings/чужого SQL.

Required proof: committed and late-arriving original request; scoped missing/foreign actor/org; revoked/restored grant; current и historical proctor group; deleted object/archived type; QR expiry; new bulk including noop/deleted division targets; old bulk409; rooms admin; corrupt/missing audit; GET не меняет state/revision/audit/guard/receipt. UI: drop настоящего post-commit response, reload, original UUID read-only recovery, zero duplicate writes, empty QR field/no sensitive storage, permission loss не теряет UUID.

## Выполненная приёмка

Native source:9 групп тестов, реальные изолированные MySQL fixtures, полный race/vet — [лог](command-receipts-u04-checks.txt). Bodyless GET не меняет ни одной строки state/audit/receipt и не вызывает Guard. Проверены поздний исходный запрос, legacy bulk409, corrupted/null/partial scope и audit, current/historical proctor, настоящее QR expiry и rotation.

Native runtime image `b8f807f40b7338cae251c9a519be9825fbff0e40134a6396193b080871436b50`, health200/OpenAPI39. Actual HTTP baseline `dev/local/attendance-command-receipts-http-report.json`:5 сценариев/925 assertions,11 committed UUID-команд в own fixtures, Core writes0 и cleanup без ошибок. Дополнительные live Core revoke/proctor writes этим прогоном не выполнялись.

BFF runtime image `853b0eb50398b6ac1ed3562feb3f99662f9a4f2c1f926da598f9ea53c2bd2473`, OpenAPI296. Отдельный review закрыл stripping RawQuery/ForceQuery: пустой `?`, malformed percent и semicolon отклоняются400 до owner call;3 регрессии, полный race/vet PASS. UI535/57/TSC/lint/optimized build PASS.

`dev/local/bff-attendance-recovery-report.json`:15 фактических браузерных сценариев PASS на own divisions10255/10256.27 normal attempts (17Core,3Attendance fixtures,7UI attempts, из них4 приняты), без повторного POST после reload/recovery. Проверены настоящий commit с потерянным ответом, exact original UUID GET и refresh, live revoke/relogin401/403, cross-tab/cabinet/actor-org fences, storage failure до dispatch, mismatch реального receipt и explicit new-intent latch. Первый прогон остановился из-за harness-only getByLabel; девять уже успешных сценариев не повторялись, tail продолжен на тех же fixtures без replay неизвестной команды. Product U04 source/runtime не менялся.

Финальный report: errors/remote/legacy/cleanup.errors пусты; роли восстановлены, обе новые Auth-сессии logout200, contexts/browser/heldGET закрыты. pending fixture/UI отсутствуют. Единственный intentional synthetic missing404 UUID сохранён UNKNOWN в private mode0600 manifest, исходного POST для него не было; отсутствие commit не заявляется. QR/body whitelist проверена source/unit; новый actual scan/QR-loss сценарий не выполнялся. Старый bulk409 и повреждения/expiry/late commit — Go/SQL proof, не browser proof. Общий QA RELEASE проекта и production cutover остаются у координатора.
