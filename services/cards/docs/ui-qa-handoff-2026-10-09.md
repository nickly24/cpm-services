# Cards: финальный owner handoff для подготовки UI QA

9 октября 2026. Это baseline готовности native Cards до нового grant enforcement; свежая реализация после прямого consent описана в `background-import-enforcement.md`. Source fingerprint ниже относится к этому baseline. Это не готовность нового механизма, не QA RELEASE и не подтверждение общей готовности фонового apply. QA следует барьеру координатора в `outputs/microservices/ui-qa-coordination.md`; до отдельного RELEASE допустимы чтение и подготовка матриц, без UI/HTTP/fixtures прогонов.

## Исходники и runtime

- Owner: чат `01a1175b-5c6e-74a0-ac1f-2de02515f350`; код `services/cards/`.
- Текущий container: `cpm-dev-cards-1`, ID `7d9d218a43e8c076313bee916f7de56cac1d8820935269fd49fd1c8a8c990160`, локальный Docker inspect: `running healthy`.
- Container image reference: `sha256:fe31a42958a6785f8e98b32c8e1bf2cc12fc9d4b60ca184a22c2536d1814860b`; Cards-only обычная Docker build/recreate завершена, другие сервисы этим владельцем не перезапускались.
- Native адрес `http://127.0.0.1:8008`; OpenAPI `1.1.0`, `/openapi.json`. Browser UI использует BFF8001, поэтому состояние UI/BFF подтверждает их владелец отдельно.
- SHA256 текущего source manifest: `2a15a478b3ddc45238d4e0110233b8114b700cf7d38ce28a38b44c2ef724140f`, 17 файлов. Manifest — отсортированные relative paths всех `*.go`, `*.sql`, `go.mod`, `go.sum`, `Dockerfile` внутри Cards; для каждого SHA256 bytes; затем SHA256 compact JSON `sort_keys=True`. Это fingerprint текущего дерева, отдельно от runtime image reference.
- Сборка Dockerfile: `CGO_ENABLED=0`, `go build -trimpath -ldflags="-s -w"`. После финальной сборки менялись только Cards documentation/harness; новые owner grant code/ключи/миграции не добавлялись.

Проверки: `go test -race ./...`, `go vet ./...` passed. Основной native HTTP harness **187 passed**, новый Uploads owner harness **227 passed**; отчёты `dev/local/cards-http-report.json`, `dev/local/cards-uploads-contract-http-report.json`. После предыдущего Core rebuild выполнен отдельный read-only smoke двух организаций1001/2001 через живые Auth/Core: readiness, active directions, manual themes и изоляция прошли. Это не UI QA. Повторять полные suites без изменения/дефекта не нужно.

На момент передачи согласовано новое Core-only окно20:41–20:43 МСК; Cards отправил явный ACK, Core dependency probes не запускает до нового Core READY. Указанный inspect не обращался к Core API.

## Контракты для трассировки

Подробности: `services/cards/README.md`, `services/cards/docs/uploads-owner-contract-2026-10-09.md`, `internal/cards/{http,dependencies,store,study,sessions,commands,model}.go`; BFF/UI — `services/bff/core-cards-integration.md` и действующие UI/BFF исходники. Legacy: `cpm-back-main/cpm_back/blueprints/{cards_bp,card_import_bp,card_transform_bp}.py` и связанные training/import/transform services. Native метод не заменяет отсутствующий UI шаг.

1. **Область данных.** Themes/cards принадлежат организации и активному направлению; у них нет division. Прогресс и настройки принадлежат account+organization. Переключение подразделения внутри организации сохраняет содержание/прогресс; другая организация изолирована. Core возвращает все active directions организации — индивидуальные назначения не вводились.
2. **Права.** Native staff `train.view/edit`; aliases `cards`/`training` совместимы. Полный admin ограничен своей организацией. Ученики изменяют только собственный прогресс. Общий Uploads lane дополнительно требует `upload`; это не новый gate для всех direct Cards действий. Transform требует `tests.edit` также при read/replay/receipt и фиксирует подтверждённое подразделение назначения.
3. **CRUD и обучение.** Тема/card CAS по revision, ключ повтора для CRUD. Fingerprint учитывает текст вопроса и ответа; изменение даёт stale/answer_changed, повтор старого learned fingerprint конфликтует. Настройки modes all/unlearned/learned/stale и batch sizes10/20/30. Удалённые cards не остаются в видимом прогрессе.
4. **Tests projection.** Cards не хранит тела Tests и не читает их Mongo напрямую. Устойчивый card ref основан на test/question ID; свежая доступность/версия/ответы приходят от Tests. Доступ к ответам определяет Tests, включая visibility и условие сдачи/окна. Нельзя обходить Tests access через Cards admin catalog.
5. **Import.** XLSX template `GET /v1/cards/imports/template`; manual targets `/imports/targets` не зависят от Tests. Parse→исправления/skip→commit, source SHA256 metadata; исходные байты/download принадлежат Uploads. Лимиты 1000 rows,10MiB весь запрос,32MiB распакованный архив; question4000/answer16000 символов. Многострочный текст сохраняется, commit обрезает наружные пробелы; duplicates являются warnings, не скрытым skip.
6. **Повторы и приватность.** Parse/edit/commit/transforms принимают UUID либо ASCII8..128 ключ. `/receipts/{key}` возвращает результат конкретной исходной команды, а не последнюю версию session. Меняется request под старым key →409. GET текущей session отдельный. Sessions/receipts/source принадлежат creator account+org; admin того же org не читает чужой preview. Каждое чтение/повтор заново проверяет права и direction; незавершённый preview TTL72h, completed результат восстанавливается и после этого.
7. **Transform.** Native JSON `{theme_id,card_ids}` выбирает до200 существующих cards; ответ≤1000 символов. Файла/файлового шаблона нет: согласовано Uploads `template=false`, GET template для cards.transform →400 `template_not_applicable`, кнопка download скрыта. Cards→Tests создаёт черновик, не публикует. Stable downstream key `cards_{session_id}` исключает второй draft при lost reply; Cards result хранит draft_id. Target division нельзя заменить сменой кабинета.

## Что остаётся непроверенным/незавершённым

- Свежий полный UI acceptance ожидает отдельного QA RELEASE и готовности UI/BFF всех владельцев. Native187+227 не закрывают UI сценарии.
- Прямое согласие на новую авторизацию получено в human message `01a12221-7d06-73f3-8f27-8c05f352977a`; Cards реализует её в согласованных границах. Owner proof/strict guards готовы в source, сетевой handshake и end-to-end ещё не завершены; runtime/keys/migrations не менялись. Старые187+227 не считаются проверкой нового grant enforcement.
- Общий worker apply с `awaiting_authorization`/`commit:false` нельзя считать успешным импортом. `cards.transform` исключён из начального background allowlist до отдельного согласования двух owners/audiences/native keys/body proofs; существующий direct Bearer flow остаётся.
- Исходники/реальная готовность общего Uploads, его templates/catalog/UI hiding и Rabbit recovery подтверждаются Uploads/BFF владельцами; согласованная применимость не равна их UI PASS.
- Production перенос, нагрузка, CI/CD и инфраструктурные выкладки этой приёмкой не охвачены.

## Правила QA и передачи дефектов

Создавать собственные синтетические fixtures после RELEASE. Не менять `dev_cards_*`, shared roles/directions/org settings и чужие данные. Revoke tests владельца уже используют fresh account+role. Общие мутации/перезапуски/fault windows согласовывать. Секреты и исходные персональные данные не включать в отчёты.

UI/BFF DTO/navigation/template proxy дефект → BFF primary owner; Cards domain/CAS/progress/session/receipt дефект → Cards primary owner; Tests access/draft boundary → один primary с вторым владельцем в копии. Воспроизводимый bug содержит UI шаги, expected/actual, роль/org/div, source/runtime, route/status и safe command/revision IDs. QA пишет владельцу+координатору, owner исправляет, QA повторяет исходный UI repro и адресную соседнюю регрессию. Исправление владельца не является QA VERIFIED.
