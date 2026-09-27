import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import type { TunnelView } from './types';
import { tunnelDisplayState } from './view-model';
import { buildConf, parseConf } from './tunnel-draft';

const fixture = (name: string) => readFileSync(path.resolve('../tests/fixtures/desktop', name), 'utf8');
for (const {view, display} of JSON.parse(fixture('status.json')) as Array<{view: TunnelView; display: string}>) {
  assert.equal(tunnelDisplayState(view), display);
}
const source = fixture('peers-before.conf');
assert.equal(buildConf(parseConf(source)), source);
const draft = parseConf(source);
draft.endpoint = 'updated.example:8443';
assert.equal(buildConf(draft), fixture('peers-after.conf'));
assert.equal(parseConf(source, 1).endpoint, '[2001:db8::3]:444');
const unknown = source + '\nconstructor = preserved\n__proto__ = preserved\n';
assert.equal(buildConf(parseConf(unknown)), unknown, 'unknown keys must not read dictionary prototypes');
