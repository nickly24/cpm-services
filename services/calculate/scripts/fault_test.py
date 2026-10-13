"""Explicit local outage check. Stops only cpm-dev's Mongo, always restarts it.

Run this separately from other HTTP suites: it intentionally makes the shared
local Mongo unavailable briefly. RabbitMQ and its durable queue stay running.
"""
import base64
from datetime import datetime, timezone
import json
from pathlib import Path
import subprocess
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mongo_client
from export_structure import read_env
from http_test import request, expect, checks, wait_for

DOCKER = '/Applications/Docker.app/Contents/Resources/bin/docker'
CONTAINER = 'cpm-dev-mongo-1'


def docker(*arguments):
    return subprocess.run([DOCKER, *arguments], check=True, capture_output=True, text=True).stdout


def main():
    config = settings()
    if config.get('RABBITMQ_HOST') != '127.0.0.1' or config.get('RABBITMQ_VHOST') != 'cpm_dev' or int(config.get('RABBITMQ_MANAGEMENT_PORT', 0)) != 15672:
        raise RuntimeError('Only fixed local cpm_dev RabbitMQ is supported')
    inspected = json.loads(docker('inspect', CONTAINER))[0]
    labels = inspected['Config']['Labels']
    if labels.get('com.docker.compose.project') != 'cpm-dev' or labels.get('com.docker.compose.service') != 'mongo':
        raise RuntimeError('Unexpected Mongo container; no outage performed')
    if inspected['State'].get('Health', {}).get('Status') != 'healthy':
        raise RuntimeError('Dev Mongo must be healthy before the outage test')
    local = read_env(ROOT / 'dev/local/calculate.env')
    headers = {'X-Service-Key': local['AUTH_INTERNAL_KEY']}
    rabbit_auth = base64.b64encode((config['RABBITMQ_USER'] + ':' + config['RABBITMQ_PASSWORD']).encode()).decode()
    rabbit_headers = {'Authorization': 'Basic ' + rabbit_auth}
    rabbit = 'http://127.0.0.1:15672/api'
    identity = str(uuid.uuid4())
    message = {'message_id': identity, 'message_type': 'tests.grade.requested', 'schema_version': 1,
               'occurred_at': datetime.now(timezone.utc).isoformat(), 'producer': 'tests-service',
               'correlation_id': str(uuid.uuid4()), 'payload': {
                   'test_id': uuid.uuid4().hex[:24], 'submission_id': uuid.uuid4().hex[:24],
                   'division_id': 1001, 'organization_id': 1001, 'account_id': 1001,
                   'input_snapshot': {'answers': [], 'question_order': []},
               }}
    wire = json.dumps(message, separators=(',', ':'))

    def publish():
        result = request(rabbit + '/exchanges/cpm_dev/cpm.events/publish', method='POST', headers=rabbit_headers,
                         body={'properties': {'delivery_mode': 2, 'content_type': 'application/json', 'message_id': identity},
                               'routing_key': 'tests.grade.requested.v1', 'payload': wire, 'payload_encoding': 'string'})
        expect(result['routed'], 'message remains routed while Mongo unavailable')

    client = mongo_client(config)
    stopped = False
    try:
        request('http://127.0.0.1:8005/health/ready')
        stopped = True
        docker('stop', '--time', '5', CONTAINER)
        publish()
        request('http://127.0.0.1:8005/health/ready', expected=503)
        request('http://127.0.0.1:8005/internal/calculate/messages/' + identity, headers=headers, expected=503)
        # Mongo's selection timeout has elapsed by now, so this delivery has
        # either returned to Rabbit or is waiting unacknowledged for the retry.
        time.sleep(2)
        queue = request(rabbit + '/queues/cpm_dev/calculate.events.v1', headers=rabbit_headers)
        expect(queue.get('messages', 0) + queue.get('messages_unacknowledged', 0) >= 1, 'failed persistence leaves message in Rabbit')
        docker('start', CONTAINER)
        stopped = False
        receipt = wait_for(lambda: client.cpm_dev.calculate_inbox_v1.find_one({'_id': identity}), timeout=40)
        expect(receipt['status'] in ['received', 'processing', 'retrying', 'skipped'], 'message delivered after Mongo recovery')
        publish()
        time.sleep(0.5)
        expect(client.cpm_dev.calculate_inbox_v1.count_documents({'_id': identity}) == 1, 'recovery and redelivery preserve one immutable receipt')
        wait_for(lambda: json.loads(docker('inspect', CONTAINER))[0]['State'].get('Health', {}).get('Status') == 'healthy', timeout=30)
        request('http://127.0.0.1:8005/health/ready')
        destination = ROOT / 'dev/local/calculate-fault-report.json'
        destination.write_text(json.dumps({'passed': len(checks), 'checks': checks, 'failure': 'temporary_local_mongo_outage', 'recovered': True}, ensure_ascii=False, indent=2) + '\n')
        print(f'Calculate Mongo outage/recovery passed: {len(checks)} checks; dev Mongo restored healthy.')
    finally:
        if stopped:
            docker('start', CONTAINER)
        try:
            client.cpm_dev.calculate_inbox_v1.delete_one({'_id': identity})
        finally:
            client.close()


if __name__ == '__main__':
    main()
