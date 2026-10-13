"""Race authoritative offline finalization against concurrent answer mirroring."""
from concurrent.futures import ThreadPoolExecutor
import copy
import json

import http_test as base
import workflow_test as flow
import grading_test as grading


def previous_schema_fixture(submission):
    previous = grading.record(submission)
    legacy_hash = flow.client_hash({'calculation_revision': 1, **previous['result']})
    client = base.mongo_client(base.settings())
    try:
        assert str(previous['test_id']) in base.ids and previous['organization_id'] == 1001
        client.cpm_dev.tests_calculation_jobs_v1.delete_many({'_id': {'$in': previous['calculation_job_ids']},
            'submission_id': previous['submission_id'], 'test_id': previous['test_id']})
        fixture = client.cpm_dev.tests_attempts_v1.update_one({'_id': previous['_id'],
            'submission_id': previous['submission_id'], 'test_id': previous['test_id'], 'calculation_status': 'ready'},
            {'$set': {'result_hash': legacy_hash}, '$unset': {name: '' for name in [
                'calculation_job_id', 'calculation_job_ids', 'grading_test_version_id', 'grading_version_index',
                'calculation_history', 'calculated_at', 'excluded_question_ids']}})
        assert fixture.matched_count == 1
    finally:
        client.close()
    return previous


def run():
    base.request('/health/ready')
    base.request('/health/ready', origin=flow.CALCULATE)
    admin = base.login('dev_admin')
    student = base.login('dev_student_01')
    direction = base.core('/directions', admin, 'POST', {'name': base.prefix + ' acceptance race'}, 201)['id']
    base.directions.append(direction)
    questions = flow.questions()
    for iteration in range(8):
        test = flow.publish(admin, grading.create(admin, direction, 'authoritative acceptance race ' + str(iteration)))
        attempt = flow.start(student, test['id'])
        final = flow.snapshot(attempt)
        alternative = copy.deepcopy(final)
        alternative['answers'][0]['value'] = 'a'
        final['snapshot_hash'] = flow.client_hash(final)
        alternative['snapshot_hash'] = flow.client_hash(alternative)
        answer = {'question_id': 1, 'value': 'a'}
        contested = iteration % 2 == 1
        bodies = [final, alternative if contested else final]
        with ThreadPoolExecutor(max_workers=3) as pool:
            mirrored = pool.submit(flow.race_request, '/v1/tests/attempts/' + attempt['id'] + '/answer', student, answer, 'PATCH')
            finished = [pool.submit(flow.race_request, '/v1/tests/attempts/' + attempt['id'] + '/finalize', student, body) for body in bodies]
            mirror_response = mirrored.result()
            outcomes = [future.result() for future in finished]
        base.expect(mirror_response[0] in [200, 409], 'concurrent mirror is recorded or sees the sealed attempt')
        codes = sorted(value[0] for value in outcomes)
        base.expect(codes == ([202, 409] if contested else [202, 202]),
                    'different final snapshots select one winner; identical snapshots both acknowledge despite mirror racing')
        accepted_index = next(index for index, value in enumerate(outcomes) if value[0] == 202)
        submission = outcomes[accepted_index][1]['data']['submission_id']
        sealed = bodies[accepted_index]
        expected = grading.legacy_grade(questions, sealed['answers'])
        flow.wait_ready(submission, student, expected['score'])
        doc = grading.record(submission)
        base.expect(doc['answers'] == sealed['answers'], 'Mongo stores the complete winning offline snapshot rather than the concurrent mirrored answer')
        grading.assert_db(submission, grading.canonical_hash(grading.immutable(doc)), expected)
        repeat = base.api('/attempts/' + attempt['id'] + '/finalize', student, 'POST', sealed, 202)
        base.expect(repeat['submission_id'] == submission, 'replayed winning snapshot returns the same physical submission')
        losing = copy.deepcopy(sealed)
        losing['answers'][0]['value'] = 'a' if sealed['answers'][0]['value'] == 'b' else 'b'
        losing.pop('snapshot_hash')
        base.api('/attempts/' + attempt['id'] + '/finalize', student, 'POST', losing, 409)

    # An administrative retry chooses a new durable calculation revision while
    # preserving the original accepted snapshot and rejecting old writes.
    retry_test = flow.publish(admin, grading.create(admin, direction, 'administrative retry'))
    flow.container('stop', 'calculate')
    try:
        answers = flow.snapshot({'test_version_id': 'unused'})['answers']
        sid, frozen, _, _ = grading.accepted(student, retry_test, answers)
        expected = grading.legacy_grade(questions, answers)
        old_callback = grading.callback(sid, expected)
        notification = {'calculation_revision': old_callback['calculation_revision'],
                        'input_hash': old_callback['input_hash'], 'status': 'failed', 'reason': 'synthetic_dependency_failure'}
        flow.internal('/internal/tests/submissions/' + sid + '/calculation-status', 'POST', notification, authorized=False, code=401)
        flow.internal('/internal/tests/submissions/' + sid + '/calculation-status', 'POST', notification)
        failed = base.api('/submissions/' + sid, student)
        base.expect(failed['calculation_status'] == 'failed' and failed['result'] is None,
                    'failed calculation keeps acceptance and raw answers without inventing a score')
        path = '/submissions/' + sid + '/retry-calculation'
        base.api(path, student, 'POST', {'expected_revision': failed['revision']}, 403)
        base.api(path, admin, 'POST', {'expected_revision': failed['revision'] - 1}, 409)
        retried = base.api(path, admin, 'POST', {'expected_revision': failed['revision']}, 202)
        base.expect(retried['calculation_revision'] == old_callback['calculation_revision'] + 1
                    and retried['calculation_status'] == 'queued' and retried['result'] is None,
                    'administrator retry durably prepares exactly one newer calculation revision')
        base.api(path, admin, 'POST', {'expected_revision': failed['revision']}, 409)
        flow.internal('/internal/tests/submissions/' + sid + '/result', 'POST', old_callback, code=409)
        flow.internal('/internal/tests/submissions/' + sid + '/calculation-status', 'POST', notification, code=409)
    finally:
        flow.container('start', 'calculate')
    flow.wait_for(lambda: base.request('/health/ready', origin=flow.CALCULATE)['status'], 'calculate restored after administrative retry', 60)
    ready = flow.wait_ready(sid, student, expected['score'], 60)
    grading.assert_db(sid, frozen, expected)
    history = base.api('/submissions/' + sid + '/calculations', admin)
    base.expect(history['total'] == 1 and history['items'][0]['calculation']['calculation_revision'] == retried['calculation_revision'],
                'history includes only the confirmed retry result, excluding failed pending jobs')
    base.api('/submissions/' + sid + '/retry-calculation', admin, 'POST', {'expected_revision': ready['revision']}, 409)
    current_callback = grading.callback(sid, expected)
    flow.internal('/internal/tests/submissions/' + sid + '/calculation-status', 'POST', {
        'calculation_revision': current_callback['calculation_revision'], 'input_hash': current_callback['input_hash'],
        'status': 'retrying', 'reason': 'delayed_status_notification'})
    base.expect(base.api('/submissions/' + sid, student)['calculation_status'] == 'ready',
                'late retrying notification cannot downgrade a completed result')

    # Emulate only our own API-created row from the receipt-only deployment.
    # It had a confirmed score but no calculation-job/audit metadata.
    legacy_test = flow.publish(admin, grading.create(admin, direction, 'previous ready schema'))
    legacy_sid, frozen, expected, _, _ = grading.grade(student, legacy_test, questions, answers)
    flow.wait_for(lambda: any(m['status'] == 'ready' for m in flow.inbox(submission_id=legacy_sid)['items']),
                  'original worker job finishes before a guarded previous-schema fixture')
    previous = previous_schema_fixture(legacy_sid)
    migrated = flow.internal('/internal/tests/submissions/' + legacy_sid + '/input')['data']
    base.expect(migrated['calculation_status'] == 'ready' and migrated['calculation_revision'] == 1,
                'previous ready session acquires job metadata while retaining its score and calculation revision')
    grading.assert_db(legacy_sid, frozen, expected)
    history = base.api('/submissions/' + legacy_sid + '/calculations', admin)
    base.expect(history['total'] == 1 and history['items'][0]['result'] == previous['result'],
                'previous ready migration exposes exactly its original confirmed output in history')

    # A content event must migrate the old confirmed score without any prior
    # lazy input request, then append the new score to that same history.
    direct_test = flow.publish(admin, grading.create(admin, direction, 'previous ready direct content event'))
    direct_sid, direct_frozen, old_expected, _, _ = grading.grade(student, direct_test, questions, answers)
    flow.wait_for(lambda: any(m['status'] == 'ready' for m in flow.inbox(submission_id=direct_sid)['items']),
                  'original job completes before direct content-event migration fixture')
    previous_schema_fixture(direct_sid)
    original_flag = base.core('/organization/test-settings', admin)['recalculate_on_test_change']
    try:
        grading.org_setting(admin, True)
        updated = grading.edit(admin, direct_test['id'], lambda b: b['questions'][0].update(points=4))
        grading.wait_change(direct_test['id'], updated['content_version_id'])
        changed_questions = copy.deepcopy(questions)
        changed_questions[0]['points'] = 4
        new_expected = grading.legacy_grade(changed_questions, answers)
        flow.wait_ready(direct_sid, student, new_expected['score'])
        grading.assert_db(direct_sid, direct_frozen, new_expected)
        history = base.api('/submissions/' + direct_sid + '/calculations', admin)
        base.expect(history['total'] == 2 and [item['result']['score'] for item in history['items']] ==
                    [new_expected['score'], old_expected['score']],
                    'direct content prepare preserves original confirmed history and appends the regraded output')
    finally:
        grading.org_setting(admin, original_flag)


if __name__ == '__main__':
    try:
        run()
    finally:
        flow.cleanup()
    report = {'status': 'passed', 'checks': len(base.checks), 'scenarios': base.checks,
              'races': 8, 'administrative_retry_verified': True,
              'previous_ready_migration_verified': True,
              'previous_ready_direct_regrade_verified': True,
              'database_snapshots_verified': True, 'synthetic_records_cleaned': True}
    path = base.ROOT / 'dev/local/tests-acceptance-race-report.json'
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    path.chmod(0o600)
    print(json.dumps({key: value for key, value in report.items() if key != 'scenarios'}))
