package tests

func (s *Server) externalSpecs() []routeSpec {
	routes := []routeSpec{
		{"GET /v1/tests/divisions/{division}/external-tests", "Внешний каталог выбранного подразделения; вопросы и локальные попытки отсутствуют", s.externalList, nil, ExternalListView{}},
		{"POST /v1/tests/divisions/{division}/external-tests", "Зарегистрировать выполненный вне CPM тест; tests.edit и Idempotency-Key", s.externalCreate, ExternalCreateDTO{}, ExternalTestView{}},
		{"GET /v1/tests/divisions/{division}/external-results", "Внешние результаты подразделения; student только себя", s.externalResults, nil, ExternalFlatListView{}},
	}
	for _, prefix := range []string{"/v1/tests/divisions/{division}/external-tests/{external}"} {
		routes = append(routes, []routeSpec{
			{"GET " + prefix, "Внешний тест; собственный результат ученика", s.externalDetail, nil, ExternalTestView{}},
			{"PUT " + prefix, "Изменить реквизиты с контролем ревизии и историей", s.externalMetadata, ExternalMetadataDTO{}, ExternalTestView{}},
			{"GET " + prefix + "/delete-preview", "Подтверждение удаления конкретной ревизии; включает число результатов", s.externalDeletePreview, nil, ExternalDeletePreview{}},
			{"DELETE " + prefix, "Логически удалить внешний тест и результаты, сохранив аудит", s.externalDelete, ExternalDeleteDTO{}, ExternalDeletedView{}},
			{"GET " + prefix + "/results", "Внешние результаты; test-results.view или student self", s.externalResultList, nil, ExternalResultListView{}},
			{"PUT " + prefix + "/results/{account}", "Административная коррекция балла; test-results.edit, ревизии и Idempotency-Key", s.externalResultWrite, ExternalResultDTO{}, ExternalTestView{}},
			{"DELETE " + prefix + "/results/{account}", "Удалить текущий результат, сохранив аудит; test-results.edit", s.externalResultWrite, ExternalResultDTO{}, ExternalTestView{}},
			{"GET " + prefix + "/history", "История внешнего теста; оценки скрыты без test-results.view", s.externalHistory, nil, ExternalHistoryListView{}},
		}...)
	}
	return routes
}
