"""Guarded local HTTP/Rabbit integration for inbox durability and practice.

Only inbox rows owned by this run are cleaned up. The run's deliberately rejected
messages stay in the dev dead-letter queue for inspection; other messages are
neither consumed nor changed by this script.
"""
import base64
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mongo_client
from export_structure import read_env

BASE = 'http://127.0.0.1:8005'
checks, message_ids = [], []


def expect(condition, label):
    if not condition:
        raise AssertionError(label)
    checks.append(label)


def request(url, method='GET', body=None, headers=None, expected=200):
    data = None if body is None else json.dumps(body).encode()
    values = dict(headers or {})
    if data is not None:
        values['Content-Type'] = 'application/json'
    req = urllib.request.Request(url, data=data, method=method, headers=values)
    try:
        with urllib.request.urlopen(req, timeout=15) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    expect(status == expected, f'{method} {urllib.parse.urlparse(url).path}: {status}')
    return json.loads(raw)


def wait_for(predicate, timeout=15):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        value = predicate()
        if value:
            return value
        time.sleep(0.2)
    raise AssertionError('Timed out waiting for persisted broker delivery')


def main():
    config = settings()
    # settings() pins local Mongo/MySQL ports. Pin Rabbit management independently.
    if config.get('RABBITMQ_HOST') != '127.0.0.1' or config.get('RABBITMQ_VHOST') != 'cpm_dev' or int(config.get('RABBITMQ_MANAGEMENT_PORT', 0)) != 15672:
        raise RuntimeError('Only fixed loopback cpm_dev RabbitMQ is supported')
    local = read_env(ROOT / 'dev/local/calculate.env')
    service_headers = {'X-Service-Key': local['AUTH_INTERNAL_KEY']}
    auth = base64.b64encode((config['RABBITMQ_USER'] + ':' + config['RABBITMQ_PASSWORD']).encode()).decode()
    rabbit_headers = {'Authorization': 'Basic ' + auth}
    rabbit = 'http://127.0.0.1:15672/api'
    vhost = urllib.parse.quote('cpm_dev', safe='')
    test_id, submission_id = uuid.uuid4().hex[:24], uuid.uuid4().hex[:24]
    database_client = mongo_client(config)
    inbox = database_client.cpm_dev.calculate_inbox_v1
    try:
        request(BASE + '/health/live')
        request(BASE + '/health/ready')
        request(BASE + '/internal/calculate/messages', expected=401)
        request(BASE + '/internal/calculate/messages', headers={'X-Service-Key': 'invalid'}, expected=401)
        request(BASE + '/internal/calculate/messages?limit=101', headers=service_headers, expected=400)
        request(BASE + '/internal/calculate/messages?submission_id=invalid', headers=service_headers, expected=400)

        queue = request(rabbit + '/queues/' + vhost + '/calculate.events.v1', headers=rabbit_headers)
        expect(queue['durable'] and queue['arguments']['x-queue-type'] == 'quorum', 'durable quorum queue')
        expect(queue['arguments'].get('x-delivery-limit') == -1, 'temporary persistence failures cannot exhaust delivery limit')
        expect(queue['arguments'].get('x-dead-letter-strategy') == 'at-least-once', 'confirmed durable dead lettering')

        def envelope(kind='tests.grade.requested'):
            identity = str(uuid.uuid4())
            message_ids.append(identity)
            return {'message_id': identity, 'message_type': kind, 'schema_version': 1,
                    'occurred_at': datetime.now(timezone.utc).isoformat(), 'producer': 'tests-service',
                    'correlation_id': str(uuid.uuid4()), 'payload': {
                        'test_id': test_id, 'submission_id': submission_id,
                        'organization_id': 1001, 'division_id': 1001, 'account_id': 1001,
                        'attempt_id': uuid.uuid4().hex[:24], 'calculation_revision': 1,
                        'input_snapshot': {'answers': [], 'question_order': [], 'reason': 'manual'},
                    }}

        def publish(body, route=None):
            wire = json.dumps(body, ensure_ascii=False, separators=(',', ':'))
            result = request(rabbit + '/exchanges/' + vhost + '/cpm.events/publish', method='POST', headers=rabbit_headers,
                             body={'properties': {'delivery_mode': 2, 'content_type': 'application/json', 'message_id': body['message_id']},
                                   'routing_key': route or body['message_type'] + '.v1', 'payload': wire, 'payload_encoding': 'string'})
            expect(result['routed'], 'persistent message routed')
            return wire.encode()

        first = envelope()
        wire = publish(first)
        wait_for(lambda: inbox.find_one({'_id': first['message_id']}))
        publish(first)
        time.sleep(0.3)
        expect(inbox.count_documents({'_id': first['message_id']}) == 1, 'identical redelivery is idempotent')
        record = request(BASE + '/internal/calculate/messages/' + first['message_id'], headers=service_headers)['data']
        expect(record['body_hash'] == hashlib.sha256(wire).hexdigest(), 'immutable exact envelope hash')
        wait_for(lambda: inbox.find_one({'_id': first['message_id'], 'status': 'skipped'}))
        expect('score' not in record and 'result' not in record, 'unknown submission cannot fabricate a result')
        expect(record['payload']['division_id'] == 1001, 'selected division preserved')
        # Simulate a crashed processor using only this run's private fake receipt.
        completed = inbox.find_one({'_id': first['message_id']})
        guarded = inbox.update_one({'_id': first['message_id'], 'body_hash': completed['body_hash'], 'status': 'skipped'},
            {'$set': {'status': 'processing', 'lease_token': 'synthetic_expired_lease',
                      'lease_until': datetime.now(timezone.utc) - timedelta(seconds=1)}})
        expect(guarded.matched_count == 1, 'expired lease fixture changes only the owned fake message')
        recovered = wait_for(lambda: inbox.find_one({'_id': first['message_id'], 'status': 'skipped',
                                                    'processing_attempts': completed['processing_attempts'] + 1}))
        expect(recovered['body'] == completed['body'] and recovered['body_hash'] == completed['body_hash'],
               'expired processing lease is recovered without rewriting the immutable envelope')
        expect('lease_token' not in recovered and 'lease_until' not in recovered, 'completed recovery releases the expired processing lease')

        changed = json.loads(json.dumps(first))
        changed['payload']['division_id'] = 1002
        publish(changed)
        invalid = envelope()
        invalid['schema_version'] = 2
        publish(invalid)

        def rejected_visible():
            values = request(rabbit + '/queues/' + vhost + '/calculate.events.dead.v1/get', method='POST', headers=rabbit_headers,
                             body={'count': 1000, 'ackmode': 'ack_requeue_true', 'encoding': 'auto', 'truncate': 4 * 1024 * 1024})
            ids = set()
            for item in values:
                try:
                    ids.add(json.loads(item['payload'])['message_id'])
                except (KeyError, json.JSONDecodeError):
                    pass
            return first['message_id'] in ids and invalid['message_id'] in ids

        wait_for(rejected_visible)
        expect(inbox.find_one({'_id': first['message_id']})['body_hash'] == hashlib.sha256(wire).hexdigest(), 'conflicting ID cannot overwrite immutable receipt')
        expect(inbox.find_one({'_id': invalid['message_id']}) is None, 'unsupported schema dead-lettered before persistence')

        second = envelope('tests.content.changed')
        publish(second)
        wait_for(lambda: inbox.find_one({'_id': second['message_id']}))
        page = request(BASE + '/internal/calculate/messages?test_id=' + test_id + '&limit=1&offset=0', headers=service_headers)['data']
        expect(page['total'] == 2 and len(page['items']) == 1, 'inbox filters and pagination')
        expect('payload' not in page['items'][0] and 'body' not in page['items'][0], 'inbox list returns metadata without loading payloads')
        expect(page['items'][0]['division_id'] == 1001 and page['items'][0]['organization_id'] == 1001, 'scope metadata preserved outside immutable body')
        detail = request(BASE + '/internal/calculate/messages/' + page['items'][0]['message_id'], headers=service_headers)['data']
        expect(detail['payload']['test_id'] == test_id, 'message detail retains full immutable payload')

        question = {'question_id': 1, 'type': 'multiple', 'text': 'Выберите правильные', 'points': 7,
                    'answers': [{'id': 'a', 'text': 'A', 'is_correct': True}, {'id': 'b', 'text': 'B', 'is_correct': False},
                                {'id': 'c', 'text': 'C', 'is_correct': True}], 'correct_answers': []}
        result = request(BASE + '/internal/calculate/practice/check', method='POST', body={'question': question, 'value': ['c', 'a']}, headers=service_headers)['data']
        expect(result == {'is_correct': True, 'points': 7, 'possible_points': 7, 'correct_answer': ['a', 'c']}, 'training exact unordered set awards full weight')
        result = request(BASE + '/internal/calculate/practice/check', method='POST', body={'question': question, 'value': ['a']}, headers=service_headers)['data']
        expect(not result['is_correct'] and result['points'] == 0, 'training no partial points')
        request(BASE + '/internal/calculate/practice/check', method='POST', body={'question': question, 'value': ['a', 'a']}, headers=service_headers, expected=400)
        finish = request(BASE + '/internal/calculate/practice/finish', method='POST', body={
            'questions': [question, {'question_id': 2, 'type': 'text', 'text': 'Введите ответ', 'points': 3,
                                     'answers': [], 'correct_answers': ['Москва']}],
            'answers': [{'question_id': 1, 'value': ['c', 'a']}],
        }, headers=service_headers)['data']
        expect(finish == {'score': 70, 'earned_points': 7, 'max_points': 10, 'question_results': [
            {'question_id': 1, 'points': 7, 'is_correct': True}, {'question_id': 2, 'points': 0, 'is_correct': False},
        ]}, 'bulk training finish grades pinned snapshot and missing questions')
        request(BASE + '/internal/calculate/practice/finish', method='POST', body={'questions': [question], 'answers': []}, expected=401)
        expect(inbox.count_documents({'_id': {'$in': message_ids}}) == 2, 'training cannot create official receipts')
        report = {'passed': len(checks), 'checks': checks, 'mode': 'grading', 'dead_letter_test_message_ids': [first['message_id'], invalid['message_id']]}
        destination = ROOT / 'dev/local/calculate-http-report.json'
        destination.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
        print(f'Calculate HTTP/Rabbit integration passed: {len(checks)} checks.')
    finally:
        inbox.delete_many({'_id': {'$in': message_ids}})
        database_client.close()


if __name__ == '__main__':
    main()
