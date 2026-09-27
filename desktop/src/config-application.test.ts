import assert from 'node:assert/strict';
import test from 'node:test';
import { ConfigurationApplications } from './config-application';

test('pending application survives restart without storing configuration or diagnostics', () => {
  const values = new Map<string, string>();
  const storage = {getItem: (key: string) => values.get(key) || null, setItem: (key: string, value: string) => { values.set(key, value); }, removeItem: (key: string) => { values.delete(key); }};
  const first = new ConfigurationApplications(storage);
  const request_id = '0123456789abcdef0123456789abcdef';
  first.set('/wg0.conf', {state: 'unknown', request_id, message: 'sensitive diagnostic'});
  assert.ok(![...values.values()].join('').includes('sensitive'));
  const reopened = new ConfigurationApplications(storage);
  assert.deepEqual(reopened.get('/wg0.conf'), {state: 'unknown', request_id});
  reopened.set('/wg0.conf', {state: 'applied'});
  assert.equal(reopened.get('/wg0.conf'), undefined);
  assert.equal(values.size, 0);
});

test('corrupt storage and blocked persistence do not prevent in-memory feedback', () => {
  const store = new ConfigurationApplications({getItem: () => '{', setItem: () => {throw new Error('blocked');}, removeItem: () => {}});
  assert.equal(store.get('wg0'), undefined);
  store.set('wg0', {state: 'saved'});
  assert.equal(store.get('wg0')?.state, 'saved');
});
