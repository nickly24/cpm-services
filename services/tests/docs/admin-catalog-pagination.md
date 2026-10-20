# Administrative test catalogue pages

Local candidate for 20 October 2026. No production deployment is implied by this document.

BFF URLs remain `GET /bff/tests/catalog` and `GET /bff/tests/bank`. Add `view=admin` to opt into the new administration read model. Without this selector the existing `ListView` contract remains unchanged. Students cannot use this mode; existing tests.view, organization, selected division and bank visibility checks apply. BFF continues to supply the selected division; browser parameters cannot choose a different tenant or cabinet.

Parameters:

- `limit`: 1–100, default20; administration UI requests6.
- `offset`: 0–100000. If the requested last page has disappeared, the server clamps once to the last available page; the response offset is authoritative.
- Existing `q` (escaped case-insensitive literal title search, max100 chars), `direction_id`, `owner_division_id`, `relation` (`all`, `owned`, `attached`, `copy`).
- `is_archived`: `true`, `false`, or `all` in this opt-in mode. `all` removes the explicit archive predicate without widening restricted staff bank visibility.
- `include_external`: `true` or `false`, defaultfalse. External tests may join the division list, never the organization bank. They are omitted for archived-only, attached/copy relations, or a different owner filter.
- `status`: `all`, `active`, `upcoming`, `ended`, `external`, `hidden`, `archived`, `bank`. Determined from the selected placement with precedence archived → not attached → unpublished → upcoming → ended → active. Missing dates remain active unless a higher-priority condition applies.
- `date_from`, `date_to`: calendar dates YYYY-MM-DD, evaluated in Europe/Moscow (UTC+03). Internal start must be at least date_from, end strictly before the day after date_to; missing internal dates remain eligible. External calendar date must lie within the inclusive range. Invalid/reversed ranges fail400.

Example response data (inside the normal status/data envelope):

```json
{
  "items": [{"test": {"id": "...", "title": "..."}}, {"external_test": {"id": "...", "name": "..."}}],
  "total": 144,
  "limit": 6,
  "offset": 132,
  "status_counts": {"all": 144, "active": 35, "hidden": 34, "upcoming": 34, "ended": 34, "external": 7},
  "server_time": "2026-10-19T14:00:00Z"
}
```

The example items are abbreviated. Each real item contains exactly one existing TestView or ExternalTestView. Counts are computed after scope/search/date/direction/relation/owner/archive filters, before status selection. Missing status keys mean0. `total` includes the selected status.

Mongo performs matching, projection, optional union, status counts and page selection. Existing order is preserved: internal tests created_at DESC/_id DESC, then external tests date DESC/_id DESC. Question bodies/answer keys, external result bodies, history and command ledgers are excluded from the read model. A single Core ReferencesBatch hydrates names/permissions only for IDs in the returned page. External results are reduced to a count solely to preserve can_delete permission.

Frontend uses one testsCatalogPage request per page change, 300ms search debounce, resets to page1 on changed filters, cancels obsolete requests and ignores late responses. No all-pages fallback is allowed. Drafts load only when their drawer is opened. The existing allTestsPages helper remains for unrelated monitoring/history consumers; the administration card list does not use it.

Release order: deploy the Tests backend first, then the scoped frontend changes. Old clients keep working against the additive backend. A new frontend against the old backend deliberately shows a version error rather than silently downloading everything. No migration, index, new secret or BFF runtime change is required. Review performance separately for unusually large catalogues; counts still evaluate all matching metadata in Mongo, while transferred items and reference hydration are bounded by limit.
