#!/usr/bin/env python3
"""Client-owned explicit Source/Knowledge workflow; no model or scheduler."""
import json
import os
import urllib.request
import uuid

base = os.environ.get('BASE_URL', 'http://localhost:8080').rstrip('/')
workspace = os.environ['WORKSPACE_ID']
key = os.environ['API_KEY']


def call(operation, **params):
    params['workspace_id'] = workspace
    if operation == 'save':
        params['idempotency_key'] = str(uuid.uuid4())
    request = urllib.request.Request(
        base + '/v1/' + operation, data=json.dumps(params).encode(),
        headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)['data']


scope1 = {'product': 'example', 'software': {'version': '1.4', 'branch': 'stable'}}
scope2 = {'product': 'example', 'software': {'version': '2.0', 'branch': 'main'}}
# Dream first checks existing knowledge. The client decides which evidence matters.
call('search', query='缓存', scope=scope1)
source1 = call('save', type='source', body='会话 A：1.4 中切换 X 后必须手动刷新缓存。', provenance='session A, message 12')
knowledge1 = call('save', type='knowledge', body='切换 X 后需要手动刷新缓存。', scope=scope1,
                  sources=[source1['id']], reason='Dream 从会话 A 提取原子事实')
source2 = call('save', type='source', body='会话 B：2.0 切换 X 时自动刷新缓存。', provenance='session B, message 8')
knowledge2 = call('save', type='knowledge', body='切换 X 时自动刷新缓存。', scope=scope2,
                  sources=[source2['id']], reason='新软件版本的独立事实')
source3 = call('save', type='source', body='会话 C：1.4 手动刷新仅在 X 从关闭切到启用时需要。', provenance='session C, message 20')
revision2 = call('save', type='knowledge', id=knowledge1['id'], expected_revision=1,
                 body='X 从关闭切到启用后，需要手动刷新缓存。', scope=scope1, conditions='X: off → on',
                 sources=[source1['id'], source3['id']], reason='补充旧软件的适用条件')
old = call('read', type='knowledge', id=knowledge1['id'], revision=1, history=True)
assert old['object']['body'] == '切换 X 后需要手动刷新缓存。'
assert old['object']['sources'] == [source1['id']]
assert call('read', type='knowledge', id=knowledge2['id'])['object']['revision'] == 1
print(json.dumps({'old_software_knowledge': revision2, 'new_software_knowledge': knowledge2,
                  'historical_revision': old['object']['revision']}, ensure_ascii=False, indent=2))
