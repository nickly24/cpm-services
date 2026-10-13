package tests

func (s *Server) attemptSpecs() []routeSpec {
	return []routeSpec{
		{"POST /v1/tests/divisions/{division}/tests/{test}/attempts", "Начать или продолжить закреплённую за подразделением попытку", s.startAttempt, StartDTO{}, AttemptView{}},
		{"GET /v1/tests/divisions/{division}/attempts", "Попытки выбранного подразделения с пагинацией", s.attemptList, nil, nil},
		{"GET /v1/tests/attempts/{attempt}", "Попытка с вопросами без ключей", s.getAttempt, nil, AttemptView{}},
		{"PATCH /v1/tests/attempts/{attempt}/answer", "Зафиксировать один ответ", s.patchAnswer, AnswerDTO{}, AttemptView{}},
		{"PATCH /v1/tests/attempts/{attempt}/answers", "Зафиксировать пачку ответов", s.batchAnswers, BatchAnswersDTO{}, AttemptView{}},
		{"POST /v1/tests/attempts/{attempt}/commits", "Повторяемая доставка офлайн-коммитов", s.commitAnswers, CommitDTO{}, nil},
		{"POST /v1/tests/attempts/{attempt}/finalize", "Принять неизменяемый снимок и поставить проверку в очередь", s.finalize, FinalizeDTO{}, SubmissionView{}},
		{"POST /v1/tests/attempts/{attempt}/submit", "Принять сдачу из сохранённых ответов", s.submitMirror, struct{}{}, SubmissionView{}},
		{"POST /v1/tests/attempts/{attempt}/force-submit", "Администратор test-results.edit сдаёт сохранённые ответы", s.forceSubmit, struct{}{}, SubmissionView{}},
		{"POST /v1/tests/attempts/{attempt}/practice-answer", "Мгновенная проверка ответа тренировки через calculate", s.practiceAnswer, AnswerDTO{}, nil},
		{"DELETE /v1/tests/attempts/{attempt}", "Сброс с защитой от устаревшего результата проверки", s.resetAttempt, RevisionDTO{}, nil},
		{"GET /v1/tests/divisions/{division}/submissions", "Сдачи выбранного подразделения с пагинацией", s.submissionList, nil, nil},
		{"GET /v1/tests/divisions/{division}/submissions/stats", "Статистика только выбранного подразделения", s.submissionStats, nil, nil},
		{"GET /v1/tests/submissions/{submission}", "Статус принятой сдачи; балл null до проверки", s.getSubmission, nil, SubmissionView{}},
		{"GET /v1/tests/submissions/{submission}/review", "Разбор готовой сдачи при разрешённом просмотре ответов", s.reviewSubmission, nil, nil},
		{"POST /v1/tests/submissions/{submission}/retry-calculation", "Повторная проверка с новой ревизией; test-results.edit", s.retryCalculation, RevisionDTO{}, SubmissionView{}},
		{"GET /v1/tests/submissions/{submission}/calculations", "История подтверждённых баллов; test-results.view", s.calculationHistory, nil, nil},
	}
}
