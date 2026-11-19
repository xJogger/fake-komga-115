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

test('WebDAV folder picker uses relPath for opening and selecting children', async () => {
  const browseStart = source.indexOf('  async function browseWebDAVDrawer(');
  const browseEnd = source.indexOf('  // --- Admin Bangumi Scraper Center ---', browseStart);
  assert.ok(browseStart >= 0 && browseEnd > browseStart);

  const parent = '/漫画 #合集';
  const child = parent + '/第 1 部';
  const fixtures = new Map([
    ['/', { current: '/', parent: '', folders: [{ name: '合集', relPath: parent }, { name: 'Invalid' }] }],
    [parent, { current: parent, parent: '/', folders: [{ name: '第 1 部', relPath: child }] }],
    [child, { current: child, parent, folders: [] }],
  ]);
  const clicks = new Map();
  const drawer = {
    html: "",
    style: {},
    get innerHTML() { return this.html; },
    set innerHTML(value) { this.html = value; clicks.clear(); },
    querySelectorAll(selector) {
      const attr = selector.slice(1, -1);
      const matches = [...this.html.matchAll(new RegExp(attr + '="([^"]*)"', 'g'))];
      const buttons = matches.map((m) => ({
        getAttribute: (name) => name === attr ? m[1] : /data-bwd-name="([^"]*)"/.exec(this.html)?.[1],
      }));
      clicks.set(attr, buttons);
      return buttons;
    },
  };
  const path = { value: '/' };
  const name = { value: '' };
  const up = {};
  const pickCurrent = {};
  const requested = [];
  const ctx = vm.createContext({
    document: { getElementById: (id) => ({
      'nl-browser-drawer': drawer, 'nl-wd-path': path, 'nl-name': name,
      'bwd-up': drawer.html.includes('id="bwd-up"') ? up : null, 'bwd-pick-cur': pickCurrent,
    })[id] },
    esc: (val) => val == null ? '' : String(val),
    api: async (url) => {
      const parsed = new URL(url, 'http://test.invalid');
      assert.equal(parsed.pathname, '/admin/api/webdav-accounts/fixture/folders');
      const path = parsed.searchParams.get('path');
      requested.push(path);
      assert.ok(fixtures.has(path), 'unexpected path: ' + path);
      return fixtures.get(path);
    },
  });
  const browse = vm.runInContext(source.slice(browseStart, browseEnd) + '\nbrowseWebDAVDrawer;', ctx);
  await browse('fixture', '/');
  assert.equal(clicks.get('data-bwd-open').length, 1);
  assert.equal(clicks.get('data-bwd-pick').length, 1);
  await clicks.get('data-bwd-open')[0].onclick();
  assert.match(drawer.html, /第 1 部/);
  await clicks.get('data-bwd-open')[0].onclick();
  assert.match(drawer.html, /无子目录/);
  await up.onclick();
  clicks.get('data-bwd-pick')[0].onclick();
  assert.deepEqual(requested, ['/', parent, child, parent]);
  assert.equal(path.value, child);
  assert.equal(name.value, '第 1 部');
  assert.equal(drawer.style.display, 'none');
});
