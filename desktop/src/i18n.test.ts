import assert from 'node:assert/strict';
import { setLanguage, t } from './i18n';
setLanguage('zh');
assert.equal(t('Status unavailable'), '暂时无法读取状态');
assert.equal(t('Edit {0}', 'Activate'), '编辑 Activate', 'user names must never be translated');
assert.equal(t('Diagnostics saved: {0}', '/tmp/{0}.zip'), '诊断已保存：/tmp/{0}.zip', 'substitution must happen only once');
assert.equal(t('constructor'), 'constructor', 'inherited dictionary keys are not messages');
setLanguage('en');
assert.equal(t('Edit {0}', '办公室'), 'Edit 办公室');
