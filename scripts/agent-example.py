#!/usr/bin/env python3
"""Execute the evidence lifecycle against an already configured workspace."""
import datetime
import json
import os
import urllib.request
import uuid

base = os.environ.get('BASE_URL', 'http://localhost:8080').rstrip('/')
workspace = os.environ['WORKSPACE_ID']
key = os.environ['API_KEY']
run = str(uuid.uuid4())


def call(operation, **params):
    params['workspace_id'] = workspace
    if operation not in ('knowledge_read', 'knowledge_history', 'knowledge_search', 'attachment_read'):
        params['idempotency_key'] = run + ':' + str(uuid.uuid4())
    request = urllib.request.Request(
        base + '/v1/operations/' + operation,
        data=json.dumps(params).encode(),
        headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)['data']


source = call('source_create', body='实验 A：条件 C 下样品表现稳定。', provenance='实验记录 A',
              source_type='experiment', tags=['示例'])
claim = call('claim_create', body='条件 C 下样品稳定。', conditions='条件 C', tags=['示例'], sources=[source])
verification = call('verification_create', body='重复实验支持该主张。', claim=claim, method='重复实验',
                    performed_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), conclusion='support',
                    output_source={'body': '重复实验的记录和测量摘要。', 'provenance': '重复实验 B',
                                   'source_type': 'experiment'})
assessment = call('assessment_create', body='已有记录在条件 C 下支持该主张。', claim=claim,
                  conclusion='support', conditions='条件 C', verifications=[verification])
knowledge = call('knowledge_publish', body='条件 C 下样品稳定；其他条件尚未评估。', conditions='条件 C',
                 tags=['示例'], claims=[claim], assessments=[assessment])

# A new counterexample is preserved alongside the support assessment.
counter_source = call('source_create', body='边界实验中出现失败，需核查条件差异。', provenance='边界实验 D', source_type='experiment')
counter = call('verification_create', body='边界实验反驳该主张。', claim=claim, method='边界条件测试',
               performed_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), conclusion='refute', evidence=[counter_source])
counter_assessment = call('assessment_create', body='边界条件下存在反例。', claim=claim,
                          conclusion='refute', conditions='边界条件', verifications=[counter])
review = call('knowledge_read', ref=knowledge, depth=4, limit=100)
events = [event['id'] for event in review['review_events']]
knowledge2 = call('knowledge_publish', id=knowledge['id'], expected_version=knowledge['version'], reason='审阅边界反例，明确限制',
                  body='条件 C 的原实验支持稳定性；边界实验存在反例，使用前需核对条件。', conditions='条件 C；边界条件不适用',
                  tags=['示例'], claims=[claim], assessments=[assessment, counter_assessment], resolve_events=events)
old = call('knowledge_read', ref=knowledge, depth=4, limit=100)
assert old['revision']['version'] == 1
current = call('knowledge_read', ref=knowledge2, depth=4, limit=100)
assert len(current['assessments']) == 2
print(json.dumps({'knowledge': knowledge2, 'historical_version': knowledge, 'remaining_review_events': current['review_events']}, ensure_ascii=False, indent=2))
