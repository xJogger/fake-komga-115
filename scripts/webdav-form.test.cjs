const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { test } = require('node:test');
const vm = require('node:vm');

const source = readFileSync('internal/httpserver/static/ui/app.js', 'utf8');
const start = source.indexOf('  function openWebDAVModal(');
const end = source.indexOf('  // --- Admin Libraries Tab ---', start);
assert.ok(start >= 0 && end > start);

const fields = {
  'wd-name': { value: 'DAV fixture' },
  'wd-url': { value: 'http://nas.example.test/dav' },
  'wd-user': { value: ' reader.demo@example.test ' },
  'wd-pass': { value: 'fixture-only' },
  'wd-rate': { value: '3' },
  'wd-insecure': { checked: false },
};

for (const account of [null, { id: 'existing-dav', name: 'DAV fixture' }]) {
  test(account ? 'editing a WebDAV account sends each API its expected fields' : 'new WebDAV account sends each API its expected fields', async () => {
    const elements = { ...fields, 'wd-modal-test': {}, 'wd-modal-save': {} };
    const requests = [];
    const context = vm.createContext({
      account,
      document: { getElementById: (key) => elements[key] },
      openModal: (opts) => opts.onMount(),
      closeModal: () => {},
      toast: (_, kind) => assert.notEqual(kind, 'error'),
      esc: String,
      api: async (url, opts) => requests.push([url, JSON.parse(JSON.stringify(opts))]),
    });
    vm.runInContext(source.slice(start, end) + '\nopenWebDAVModal(account, () => {});', context);
    await elements['wd-modal-test'].onclick();
    await elements['wd-modal-save'].onclick();

    const connection = { baseUrl: 'http://nas.example.test/dav', username: 'reader.demo@example.test',
      password: 'fixture-only', ratePerSecond: 3, insecureTls: false };
    assert.deepEqual(requests, [
      ['/admin/api/webdav-accounts/test', { method: 'POST',
        body: account ? { ...connection, id: account.id } : connection }],
      ['/admin/api/webdav-accounts' + (account ? '/existing-dav' : ''), {
        method: account ? 'PUT' : 'POST', body: { ...connection, name: 'DAV fixture' } }],
    ]);
  });
}
