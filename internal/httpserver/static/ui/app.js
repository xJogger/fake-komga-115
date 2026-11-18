(() => {
  'use strict';

  const THEME_MODE_KEY = 'fk115_theme_mode';
  const darkMediaQuery = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

  const state = {
    auth: { claimed: true, authenticated: false, username: '' },
    themeMode: localStorage.getItem(THEME_MODE_KEY) || 'auto',
    theme: 'dark',
    libraries: [],
    taxonomies: { loaded: false, genres: [], tags: [], publishers: [] },
    pollTimer: null,
    pollFn: null,
    coverVer: Date.now(),
    adminTab: 'overview',
    bgmProxyProbeHTML: '',
  };

  function resolveEffectiveTheme(mode) {
    if (mode === 'light' || mode === 'dark') return mode;
    return darkMediaQuery && darkMediaQuery.matches ? 'dark' : 'light';
  }

  function themeButtonLabel(mode) {
    if (mode === 'light') return '☀ 日间';
    if (mode === 'dark') return '☾ 夜间';
    return '◐ 自动';
  }

  function themeButtonTitle(mode) {
    if (mode === 'light') return '当前：固定日间模式（点击切换为夜间）';
    if (mode === 'dark') return '当前：固定夜间模式（点击切换为自动跟随系统）';
    return '当前：自动跟随系统日夜模式（点击切换为日间）';
  }

  function nextThemeMode(mode) {
    if (mode === 'auto') return 'light';
    if (mode === 'light') return 'dark';
    return 'auto';
  }

  function syncThemeButtons() {
    document.querySelectorAll('[data-theme-btn]').forEach((btn) => {
      btn.textContent = themeButtonLabel(state.themeMode);
      btn.title = themeButtonTitle(state.themeMode);
    });
  }

  function setThemeMode(mode) {
    state.themeMode = (mode === 'light' || mode === 'dark') ? mode : 'auto';
    state.theme = resolveEffectiveTheme(state.themeMode);
    document.documentElement.setAttribute('data-theme', state.theme);
    localStorage.setItem(THEME_MODE_KEY, state.themeMode);
    syncThemeButtons();
  }

  if (darkMediaQuery) {
    const onSysChange = () => {
      if (state.themeMode === 'auto') {
        state.theme = resolveEffectiveTheme('auto');
        document.documentElement.setAttribute('data-theme', state.theme);
      }
    };
    if (typeof darkMediaQuery.addEventListener === 'function') {
      darkMediaQuery.addEventListener('change', onSysChange);
    } else if (typeof darkMediaQuery.addListener === 'function') {
      darkMediaQuery.addListener(onSysChange);
    }
  }
  setThemeMode(state.themeMode);

  // Sync visualViewport height into --vvh and track mobile keyboard focus to prevent
  // Android WebView (e.g. Mihon) vertical layout oscillations while typing.
  function isEditableInput(el) {
    if (!el || !el.tagName) return false;
    if (el.tagName === 'TEXTAREA') return !el.readOnly && !el.disabled;
    if (el.tagName === 'INPUT') {
      const t = (el.type || 'text').toLowerCase();
      if (['button', 'checkbox', 'radio', 'submit', 'reset', 'file', 'hidden', 'range', 'color'].includes(t)) return false;
      return !el.readOnly && !el.disabled;
    }
    return !!el.isContentEditable;
  }

  function focusIfDesktop(el) {
    if (!el) return;
    if (window.matchMedia && window.matchMedia('(pointer: fine)').matches) {
      el.focus();
    }
  }

  (() => {
    let lastVVH = 0;
    let vvRaf = 0;
    const syncVVH = () => {
      vvRaf = 0;
      const h = Math.round((window.visualViewport ? window.visualViewport.height : window.innerHeight) || 0);
      if (h > 0 && Math.abs(h - lastVVH) >= 1) {
        lastVVH = h;
        document.documentElement.style.setProperty('--vvh', `${h}px`);
      }
    };
    const scheduleVVH = () => {
      if (!vvRaf) vvRaf = window.requestAnimationFrame(syncVVH);
    };
    syncVVH();
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', scheduleVVH, { passive: true });
    }
    window.addEventListener('resize', scheduleVVH, { passive: true });

    let blurTimer = null;
    document.addEventListener('focusin', (e) => {
      if (blurTimer) {
        clearTimeout(blurTimer);
        blurTimer = null;
      }
      if (isEditableInput(e.target)) {
        document.documentElement.classList.add('kb-typing');
        scheduleVVH();
      } else {
        document.documentElement.classList.remove('kb-typing');
      }
    });
    document.addEventListener('focusout', () => {
      if (blurTimer) clearTimeout(blurTimer);
      blurTimer = setTimeout(() => {
        blurTimer = null;
        if (!isEditableInput(document.activeElement)) {
          document.documentElement.classList.remove('kb-typing');
        }
        scheduleVVH();
      }, 120);
    });
  })();

  function esc(val) {
    if (val === null || val === undefined) return '';
    return String(val)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function fmtBytes(n) {
    const v = Number(n || 0);
    if (!v || v < 1024) return `${v || 0} B`;
    const units = ['KB', 'MB', 'GB', 'TB'];
    let size = v;
    for (const u of units) {
      size /= 1024;
      if (size < 1024 || u === 'TB') return `${size.toFixed(1)} ${u}`;
    }
    return `${v} B`;
  }

  function fmtMiB(n) {
    const mib = Number(n || 0) / (1024 * 1024);
    if (!mib) return '0.0 MiB';
    if (mib < 1) return `${mib.toFixed(2)} MiB`;
    return `${mib.toFixed(1)} MiB`;
  }

  function bytesToMiBInput(rawBytes, fallbackMiB) {
    const n = Number(rawBytes);
    if (!Number.isFinite(n) || n < 0) return String(fallbackMiB);
    const mib = n / (1024 * 1024);
    const rounded = Math.round(mib * 100) / 100;
    return String(rounded);
  }

  function mibInputToBytes(rawVal) {
    const num = parseFloat(String(rawVal || '').trim());
    if (!Number.isFinite(num) || num < 0) return null;
    return String(Math.round(num * 1024 * 1024));
  }

  function fmtTime(iso) {
    if (!iso) return '—';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return String(iso);
    return d.toLocaleString('zh-CN', { hour12: false });
  }

  function providerLabel(p) {
    switch ((p || '115').toLowerCase()) {
      case 'local': return '本地 / SMB';
      case 'webdav': return 'WebDAV';
      default: return '115 网盘';
    }
  }

  function toast(msg, kind = 'ok') {
    const root = document.getElementById('toast-root');
    if (!root) return;
    const el = document.createElement('div');
    el.className = `toast ${kind === 'error' ? 'toast-error' : 'toast-ok'}`;
    el.textContent = msg;
    root.appendChild(el);
    setTimeout(() => { el.remove(); }, 3600);
  }

  function stopPolling() {
    if (state.pollTimer) {
      clearTimeout(state.pollTimer);
      state.pollTimer = null;
    }
    state.pollFn = null;
  }

  function schedulePoll(fn, ms = 2600) {
    if (state.pollTimer) {
      clearTimeout(state.pollTimer);
      state.pollTimer = null;
    }
    state.pollFn = fn;
    if (document.hidden) return;
    state.pollTimer = setTimeout(() => {
      state.pollTimer = null;
      if (!document.hidden && state.pollFn === fn) {
        fn();
      }
    }, ms);
  }

  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && state.pollFn && !state.pollTimer) {
      state.pollFn();
    }
  });

  async function api(url, opts = {}) {
    const headers = Object.assign({ 'X-FK115-UI': '1' }, opts.headers || {});
    const fetchOpts = {
      method: opts.method || 'GET',
      credentials: 'same-origin',
      headers,
    };
    if (opts.body !== undefined) {
      headers['Content-Type'] = 'application/json';
      fetchOpts.body = JSON.stringify(opts.body);
    }
    const resp = await fetch(url, fetchOpts);
    if (resp.status === 401 && !opts.allow401) {
      let errData = {};
      try { errData = await resp.json(); } catch (_) {}
      if (errData.error === 'SETUP_REQUIRED') {
        navigate('/setup', true);
      } else if (location.pathname !== '/login') {
        const redir = encodeURIComponent(location.pathname + location.search);
        navigate(`/login?redirect=${redir}`, true);
      }
      throw new Error(errData.message || '请先登录');
    }
    if (resp.status === 204) return null;
    const ct = resp.headers.get('content-type') || '';
    const data = ct.includes('application/json') ? await resp.json() : await resp.text();
    if (!resp.ok) {
      const msg = (data && typeof data === 'object' && (data.message || data.error)) || `请求失败 (${resp.status})`;
      const err = new Error(msg);
      err.status = resp.status;
      err.code = data && data.error;
      throw err;
    }
    return data;
  }

  let modalScroll = null;
  function closeModal() {
    const root = document.getElementById('modal-root');
    if (root) {
      if (root.contains(document.activeElement)) document.activeElement.blur();
      root.innerHTML = '';
    }
    document.body.classList.remove('modal-open');
    if (modalScroll) {
      const { x, y } = modalScroll;
      modalScroll = null;
      if (window.scrollX !== x || window.scrollY !== y) window.scrollTo(x, y);
    }
  }

  function openModal({ title, subheadHTML = '', bodyHTML, footHTML = '', maxWidth = '760px', onMount }) {
    const root = document.getElementById('modal-root');
    if (!modalScroll) modalScroll = { x: window.scrollX, y: window.scrollY };
    document.body.classList.add('modal-open');
    root.innerHTML = `
      <div class="modal-backdrop" id="modal-backdrop">
        <div class="modal-dialog" style="max-width:${esc(maxWidth)}">
          <div class="modal-head">
            <h3>${esc(title)}</h3>
            <button type="button" class="btn btn-sm" style="flex-shrink:0" id="modal-close-btn">关闭</button>
          </div>
          ${subheadHTML ? `<div class="modal-subhead" id="modal-subhead">${subheadHTML}</div>` : ''}
          <div class="modal-body" id="modal-body">${bodyHTML}</div>
          ${footHTML ? `<div class="modal-foot">${footHTML}</div>` : ''}
        </div>
      </div>`;
    document.getElementById('modal-close-btn').onclick = closeModal;
    document.getElementById('modal-backdrop').addEventListener('click', (e) => {
      if (e.target.id === 'modal-backdrop') closeModal();
    });
    if (onMount) onMount(document.getElementById('modal-body'));
  }

  function navigate(target, replace = false) {
    stopPolling();
    closeModal();
    if (replace) {
      history.replaceState(null, '', target);
    } else {
      history.pushState(null, '', target);
    }
    route();
  }

  document.addEventListener('click', (e) => {
    const a = e.target.closest('a[data-nav]');
    if (!a) return;
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
    const href = a.getAttribute('href');
    if (!href || href.startsWith('/reader/') || href.startsWith('http')) return;
    e.preventDefault();
    navigate(href);
  });

  window.addEventListener('popstate', () => {
    stopPolling();
    closeModal();
    route();
  });

  function renderShell(activeNav, innerHTML, searchVal = '') {
    const app = document.getElementById('app');
    app.innerHTML = `
      <header class="topbar">
        <div class="topbar-inner">
          <a href="/" data-nav class="brand">
            <span class="brand-mark">FK</span>
            <span>fake-komga-115</span>
          </a>
          <nav class="topnav">
            <a href="/" data-nav class="${activeNav === 'home' ? 'active' : ''}">书架</a>
            <a href="/libraries" data-nav class="${activeNav === 'libraries' ? 'active' : ''}">浏览书库</a>
            <a href="/admin" data-nav class="${activeNav === 'admin' ? 'active' : ''}">管理后台</a>
          </nav>
          <form class="topbar-search" id="global-search-form">
            <span class="search-icon">⌕</span>
            <input type="text" id="global-search-input" placeholder="搜索书名、原目录名、作者…" value="${esc(searchVal)}">
          </form>
          <div class="topbar-actions">
            <button type="button" class="btn btn-sm" id="theme-toggle-btn" data-theme-btn title="${esc(themeButtonTitle(state.themeMode))}">${esc(themeButtonLabel(state.themeMode))}</button>
            ${state.auth.username ? `<span class="user-pill" title="当前登录账号">${esc(state.auth.username)}</span>` : ''}
            <button type="button" class="btn btn-sm" id="logout-btn">退出</button>
          </div>
        </div>
      </header>
      <main class="page-shell" id="page-content">${innerHTML}</main>
    `;
    document.getElementById('theme-toggle-btn').onclick = () => {
      setThemeMode(nextThemeMode(state.themeMode));
    };
    document.getElementById('logout-btn').onclick = async () => {
      try { await api('/admin/api/auth/logout', { method: 'POST', allow401: true }); } catch (_) {}
      state.auth.authenticated = false;
      navigate('/login', true);
    };
    document.getElementById('global-search-form').onsubmit = (e) => {
      e.preventDefault();
      const q = document.getElementById('global-search-input').value.trim();
      navigate(q ? `/libraries?q=${encodeURIComponent(q)}` : '/libraries');
    };
  }

  function seriesCardHTML(s) {
    const meta = s.metadata || {};
    const title = meta.title || s.name || '未命名';
    const origDiff = s.name && s.name !== title ? s.name : '';
    const unread = Number(s.booksUnreadCount || 0);
    const total = Number(s.booksCount || 0);
    const read = Number(s.booksReadCount || 0);
    const pct = total > 0 ? Math.min(100, Math.round((read / total) * 100)) : 0;
    const score = typeof s.bgmScore === 'number' && s.bgmScore > 0 ? s.bgmScore.toFixed(1) : '';
    return `
      <a href="/series/${encodeURIComponent(s.id)}" data-nav class="poster-card">
        <div class="poster-thumb">
          <img loading="lazy" src="/api/v1/series/${encodeURIComponent(s.id)}/thumbnail?v=${state.coverVer}" alt="${esc(title)}">
          ${score ? `<span class="poster-badge-top-left">★ ${esc(score)}</span>` : ''}
          ${unread > 0 ? `<span class="poster-badge-top-right">${unread}</span>` : ''}
          ${read > 0 ? `<div class="poster-progress-bar"><span style="width:${pct}%"></span></div>` : ''}
        </div>
        <div class="poster-body">
          <div class="poster-title" title="${esc(title)}">${esc(title)}</div>
          <div class="poster-sub" title="${esc(origDiff || `${total} 册`)}">${esc(origDiff || (s.oneshot ? '单本' : `共 ${total} 册`))}</div>
        </div>
      </a>
    `;
  }

  // --- Setup & Login Views ---

  function renderSetup() {
    document.title = '初始化管理员 · fake-komga-115';
    const app = document.getElementById('app');
    app.innerHTML = `
      <div class="auth-screen">
        <form class="auth-card" id="setup-form">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:8px">
            <div class="brand"><span class="brand-mark">FK</span><span>fake-komga-115</span></div>
            <button type="button" class="btn btn-sm" id="setup-theme-btn" data-theme-btn title="${esc(themeButtonTitle(state.themeMode))}">${esc(themeButtonLabel(state.themeMode))}</button>
          </div>
          <h1>初始化管理员账号</h1>
          <p class="lead">欢迎使用 v2。首次启用或升级后请设置管理员用户名与登录密码，设置完成后书架与 Komga 接口均受鉴权保护。</p>
          <div class="field">
            <label for="setup-user">管理员用户名</label>
            <input id="setup-user" type="text" autocomplete="username" placeholder="请自定义用户名（2-64 个字符）" required>
          </div>
          <div class="field">
            <label for="setup-pass">登录密码</label>
            <input id="setup-pass" type="password" autocomplete="new-password" placeholder="至少 8 位字符" required>
          </div>
          <div class="field">
            <label for="setup-pass2">确认登录密码</label>
            <input id="setup-pass2" type="password" autocomplete="new-password" placeholder="再次输入登录密码" required>
          </div>
          <button type="submit" class="btn btn-primary btn-lg" style="width:100%;margin-top:6px" id="setup-submit">完成初始化并进入书架</button>
        </form>
      </div>`;
    document.getElementById('setup-theme-btn').onclick = () => setThemeMode(nextThemeMode(state.themeMode));
    focusIfDesktop(document.getElementById('setup-user'));
    document.getElementById('setup-form').onsubmit = async (e) => {
      e.preventDefault();
      const username = document.getElementById('setup-user').value.trim();
      const password = document.getElementById('setup-pass').value;
      const password2 = document.getElementById('setup-pass2').value;
      if (username.length < 2) { toast('用户名至少需要 2 个字符', 'error'); return; }
      if (password.length < 8) { toast('密码至少需要 8 位字符', 'error'); return; }
      if (password !== password2) { toast('两次输入的密码不一致', 'error'); return; }
      const btn = document.getElementById('setup-submit');
      btn.disabled = true;
      try {
        const res = await api('/admin/api/auth/setup', {
          method: 'POST',
          body: { username, password },
          allow401: true,
        });
        state.auth = { claimed: true, authenticated: true, username: res.username };
        toast('管理员账号设置成功');
        navigate('/', true);
      } catch (err) {
        toast(err.message, 'error');
        btn.disabled = false;
      }
    };
  }

  function renderLogin() {
    document.title = '登录 · fake-komga-115';
    const params = new URLSearchParams(location.search);
    const redirect = params.get('redirect') || '/';
    const app = document.getElementById('app');
    app.innerHTML = `
      <div class="auth-screen">
        <form class="auth-card" id="login-form">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:8px">
            <div class="brand"><span class="brand-mark">FK</span><span>fake-komga-115</span></div>
            <button type="button" class="btn btn-sm" id="login-theme-btn" data-theme-btn title="${esc(themeButtonTitle(state.themeMode))}">${esc(themeButtonLabel(state.themeMode))}</button>
          </div>
          <h1>登录书架</h1>
          <p class="lead">请输入管理员用户名与密码访问漫画书库、内置阅读器与管理后台。</p>
          <div class="field">
            <label for="login-user">用户名</label>
            <input id="login-user" type="text" autocomplete="username" placeholder="管理员用户名" required>
          </div>
          <div class="field">
            <label for="login-pass">密码</label>
            <input id="login-pass" type="password" autocomplete="current-password" placeholder="登录密码" required>
          </div>
          <button type="submit" class="btn btn-primary btn-lg" style="width:100%;margin-top:6px" id="login-submit">登录</button>
        </form>
      </div>`;
    document.getElementById('login-theme-btn').onclick = () => setThemeMode(nextThemeMode(state.themeMode));
    focusIfDesktop(document.getElementById('login-user'));
    document.getElementById('login-form').onsubmit = async (e) => {
      e.preventDefault();
      const username = document.getElementById('login-user').value.trim();
      const password = document.getElementById('login-pass').value;
      const btn = document.getElementById('login-submit');
      btn.disabled = true;
      try {
        const res = await api('/admin/api/auth/login', {
          method: 'POST',
          body: { username, password },
          allow401: true,
        });
        state.auth = { claimed: true, authenticated: true, username: res.username };
        navigate(redirect.startsWith('/') && !redirect.startsWith('//') ? redirect : '/', true);
      } catch (err) {
        toast(err.message || '用户名或密码错误', 'error');
        btn.disabled = false;
      }
    };
  }

  // --- Home View (/) ---

  async function renderHome() {
    document.title = '书架 · fake-komga-115';
    renderShell('home', `<div class="boot-splash" style="min-height:45vh"><div class="spinner"></div><p>正在读取书架…</p></div>`);
    try {
      const [libs, inProgress, recentUpdated, recentAdded] = await Promise.all([
        api('/api/v1/libraries'),
        api('/api/v1/books?read_status=IN_PROGRESS&sort=readProgress.readDate,desc&size=8'),
        api('/api/v1/series?sort=lastModifiedDate,desc&size=18'),
        api('/api/v1/series?sort=createdDate,desc&size=12'),
      ]);
      state.libraries = libs || [];
      const contBooks = (inProgress && inProgress.content) || [];
      const updatedList = (recentUpdated && recentUpdated.content) || [];
      const addedList = (recentAdded && recentAdded.content) || [];

      let html = '';
      if (state.libraries.length === 0) {
        html += `
          <div class="card" style="text-align:center;padding:38px 20px">
            <h2>欢迎来到 fake-komga-115 书架</h2>
            <p style="color:var(--text-secondary);max-width:540px;margin:8px auto 18px">
              当前暂无可展示的书库（尚未创建书库，或所有书库均已设为隐藏）。支持挂载 <strong>115 网盘目录</strong>、<strong>本地 / SMB 挂载目录</strong> 与 <strong>WebDAV 远程目录（CloudDrive2 / OpenList / NAS）</strong>，并支持 Bangumi 中文元数据刮削与内置简体中文双页阅读器。
            </p>
            <a href="/admin?tab=libraries" data-nav class="btn btn-primary btn-lg">前往管理后台管理书库</a>
          </div>`;
      } else {
        html += `
          <section class="section-block">
            <div class="section-head">
              <h2>书库</h2>
              <a href="/libraries" data-nav class="more-link">浏览全部 →</a>
            </div>
            <div class="lib-strip">
              <a href="/libraries" data-nav class="lib-pill">全部书库</a>
              ${state.libraries.map((l) => `
                <a href="/libraries/${encodeURIComponent(l.id)}" data-nav class="lib-pill">
                  <span>${esc(l.name)}</span>
                  <span class="badge">${esc(providerLabel(l.providerType))}</span>
                  ${l.oneshotsDirectory ? `<span class="badge badge-accent">单本</span>` : ''}
                </a>
              `).join('')}
            </div>
          </section>`;
      }

      if (contBooks.length > 0) {
        html += `
          <section class="section-block">
            <div class="section-head">
              <h2>继续阅读</h2>
            </div>
            <div class="continue-grid">
              ${contBooks.map((b) => {
                const pg = (b.readProgress && b.readProgress.page) || 1;
                const totalPg = (b.media && b.media.pagesCount) || 0;
                const readerURL = `/reader/series/${encodeURIComponent(b.seriesId)}/book/${encodeURIComponent(b.id)}?page=${pg}`;
                return `
                  <div class="continue-card">
                    <a href="/series/${encodeURIComponent(b.seriesId)}" data-nav>
                      <img loading="lazy" src="/api/v1/series/${encodeURIComponent(b.seriesId)}/thumbnail?v=${state.coverVer}" alt="">
                    </a>
                    <div class="continue-info">
                      <a href="/series/${encodeURIComponent(b.seriesId)}" data-nav class="continue-series" title="${esc(b.seriesTitle)}">${esc(b.seriesTitle || b.name)}</a>
                      <div class="continue-book" title="${esc(b.name)}">${esc(b.metadata && b.metadata.title ? b.metadata.title : b.name)}</div>
                      <div><span class="badge badge-accent">第 ${pg}${totalPg > 0 ? ` / ${totalPg}` : ''} 页</span></div>
                    </div>
                    <a href="${readerURL}" class="btn btn-primary btn-sm">继续读</a>
                  </div>`;
              }).join('')}
            </div>
          </section>`;
      }

      if (updatedList.length > 0) {
        html += `
          <section class="section-block">
            <div class="section-head">
              <h2>最近更新</h2>
              <a href="/libraries?sort=lastModifiedDate,desc" data-nav class="more-link">更多 →</a>
            </div>
            <div class="poster-grid">${updatedList.map(seriesCardHTML).join('')}</div>
          </section>`;
      }

      if (addedList.length > 0) {
        html += `
          <section class="section-block">
            <div class="section-head">
              <h2>最新入库</h2>
              <a href="/libraries?sort=createdDate,desc" data-nav class="more-link">更多 →</a>
            </div>
            <div class="poster-grid">${addedList.map(seriesCardHTML).join('')}</div>
          </section>`;
      }

      const container = document.getElementById('page-content');
      if (container) container.innerHTML = html || `<div class="empty-box">暂无漫画条目，请先在后台扫描书库。</div>`;
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  // --- Libraries Browse View (/libraries & /libraries/:id) ---

  async function ensureTaxonomies() {
    if (state.taxonomies.loaded) return;
    try {
      const [genres, tags, publishers] = await Promise.all([
        api('/api/v1/genres'),
        api('/api/v1/tags'),
        api('/api/v1/publishers'),
      ]);
      state.taxonomies = {
        loaded: true,
        genres: genres || [],
        tags: tags || [],
        publishers: publishers || [],
      };
    } catch (_) {}
  }

  async function renderLibraries(libraryID) {
    const sp = new URLSearchParams(location.search);
    const q = sp.get('q') || '';
    const readStatus = sp.get('read_status') || '';
    const scrapeStatus = sp.get('scrape_status') || '';
    const oneshot = sp.get('oneshot') || '';
    const seriesStatus = sp.get('status') || '';
    const genre = sp.get('genre') || '';
    const tag = sp.get('tag') || '';
    const publisher = sp.get('publisher') || '';
    const author = sp.get('author') || '';
    const sort = sp.get('sort') || 'lastModifiedDate,desc';
    const page = Math.max(0, parseInt(sp.get('page') || '0', 10) || 0);
    const size = 36;

    document.title = '浏览书库 · fake-komga-115';
    renderShell('libraries', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在加载漫画列表…</p></div>`, q);

    try {
      const [libs] = await Promise.all([api('/api/v1/libraries'), ensureTaxonomies()]);
      state.libraries = libs || [];

      const apiParams = new URLSearchParams();
      apiParams.set('page', String(page));
      apiParams.set('size', String(size));
      apiParams.set('sort', sort);
      if (libraryID) apiParams.set('library_id', libraryID);
      if (q) apiParams.set('search', q);
      if (readStatus) apiParams.set('read_status', readStatus);
      if (scrapeStatus) apiParams.set('scrape_status', scrapeStatus);
      if (oneshot !== '') apiParams.set('oneshot', oneshot);
      if (seriesStatus) apiParams.set('status', seriesStatus);
      if (genre) apiParams.set('genre', genre);
      if (tag) apiParams.set('tag', tag);
      if (publisher) apiParams.set('publisher', publisher);
      if (author) apiParams.set('author', author);

      const res = await api(`/api/v1/series?${apiParams.toString()}`);
      const items = (res && res.content) || [];
      const totalElements = Number((res && res.totalElements) || 0);
      const totalPages = Number((res && res.totalPages) || 0);

      const buildURL = (overrides) => {
        const nextLib = overrides.libraryID !== undefined ? overrides.libraryID : libraryID;
        const base = nextLib ? `/libraries/${encodeURIComponent(nextLib)}` : '/libraries';
        const nextSp = new URLSearchParams(location.search);
        Object.entries(overrides).forEach(([k, v]) => {
          if (k === 'libraryID') return;
          if (v === '' || v === null || v === undefined) nextSp.delete(k);
          else nextSp.set(k, String(v));
        });
        if (overrides.page === undefined) nextSp.delete('page');
        const qs = nextSp.toString();
        return qs ? `${base}?${qs}` : base;
      };

      const content = document.getElementById('page-content');
      if (!content) return;
      content.innerHTML = `
        <div class="section-block" style="margin-bottom:16px">
          <div class="lib-strip">
            <a href="${esc(buildURL({ libraryID: '' }))}" data-nav class="lib-pill ${!libraryID ? 'active' : ''}">全部书库</a>
            ${state.libraries.map((l) => `
              <a href="${esc(buildURL({ libraryID: l.id }))}" data-nav class="lib-pill ${libraryID === l.id ? 'active' : ''}">
                <span>${esc(l.name)}</span>
                <span class="badge">${esc(providerLabel(l.providerType))}</span>
              </a>
            `).join('')}
          </div>
        </div>

        <div class="filter-panel">
          <div class="filter-row">
            <input type="text" id="f-q" class="filter-search-input" placeholder="关键词（中文译名 / 原文件夹名 / 别名 / 作者）" value="${esc(q)}">
            <select id="f-sort">
              <option value="lastModifiedDate,desc" ${sort === 'lastModifiedDate,desc' ? 'selected' : ''}>按最近更新</option>
              <option value="createdDate,desc" ${sort === 'createdDate,desc' ? 'selected' : ''}>按最新入库</option>
              <option value="metadata.titleSort,asc" ${sort === 'metadata.titleSort,asc' ? 'selected' : ''}>按标题 A-Z</option>
              <option value="bgmScore,desc" ${sort === 'bgmScore,desc' ? 'selected' : ''}>按 Bangumi 评分</option>
              <option value="random" ${sort === 'random' ? 'selected' : ''}>随机漫游</option>
            </select>
            <select id="f-read">
              <option value="">阅读状态：全部</option>
              <option value="UNREAD" ${readStatus === 'UNREAD' ? 'selected' : ''}>未读</option>
              <option value="IN_PROGRESS" ${readStatus === 'IN_PROGRESS' ? 'selected' : ''}>阅读中</option>
              <option value="READ" ${readStatus === 'READ' ? 'selected' : ''}>已读完</option>
            </select>
            <select id="f-scrape">
              <option value="">Bangumi：全部</option>
              <option value="matched" ${scrapeStatus === 'matched' ? 'selected' : ''}>已匹配 Bangumi</option>
              <option value="manual" ${scrapeStatus === 'manual' ? 'selected' : ''}>手动指定</option>
              <option value="unmatched" ${scrapeStatus === 'unmatched' ? 'selected' : ''}>自动未命中</option>
              <option value="none" ${(scrapeStatus === 'none' || scrapeStatus === 'pending') ? 'selected' : ''}>尚未刮削</option>
            </select>
            <select id="f-oneshot">
              <option value="">形态：全部</option>
              <option value="false" ${oneshot === 'false' ? 'selected' : ''}>多卷系列</option>
              <option value="true" ${oneshot === 'true' ? 'selected' : ''}>单本 One-Shot</option>
            </select>
          </div>
          <div class="filter-row">
            <select id="f-status">
              <option value="">连载状态：全部</option>
              <option value="ONGOING" ${seriesStatus === 'ONGOING' ? 'selected' : ''}>连载中</option>
              <option value="ENDED" ${seriesStatus === 'ENDED' ? 'selected' : ''}>已完结</option>
              <option value="HIATUS" ${seriesStatus === 'HIATUS' ? 'selected' : ''}>休刊</option>
              <option value="ABANDONED" ${seriesStatus === 'ABANDONED' ? 'selected' : ''}>中止</option>
            </select>
            <select id="f-genre">
              <option value="">类型：全部</option>
              ${state.taxonomies.genres.map((g) => `<option value="${esc(g)}" ${genre === g ? 'selected' : ''}>${esc(g)}</option>`).join('')}
            </select>
            <select id="f-tag">
              <option value="">标签：全部</option>
              ${state.taxonomies.tags.slice(0, 150).map((t) => `<option value="${esc(t)}" ${tag === t ? 'selected' : ''}>${esc(t)}</option>`).join('')}
            </select>
            <select id="f-pub">
              <option value="">出版社：全部</option>
              ${state.taxonomies.publishers.slice(0, 150).map((p) => `<option value="${esc(p)}" ${publisher === p ? 'selected' : ''}>${esc(p)}</option>`).join('')}
            </select>
            <input type="text" id="f-author" class="filter-author-input" placeholder="作者筛选" value="${esc(author)}">
            <button type="button" class="btn btn-primary btn-sm" id="f-apply">筛选</button>
            <button type="button" class="btn btn-sm" id="f-reset">重置</button>
            <span class="filter-count" style="margin-left:auto;color:var(--text-muted);font-size:12.5px">共 ${totalElements.toLocaleString()} 部作品</span>
          </div>
        </div>

        ${items.length > 0
          ? `<div class="poster-grid">${items.map(seriesCardHTML).join('')}</div>`
          : `<div class="empty-box">没有找到符合筛选条件的漫画作品。</div>`}

        ${totalPages > 1 ? `
          <div class="pagination">
            <button type="button" class="btn btn-sm" id="pg-prev" ${page <= 0 ? 'disabled' : ''}>上一页</button>
            <span style="color:var(--text-secondary)">第 ${page + 1} / ${totalPages} 页</span>
            <button type="button" class="btn btn-sm" id="pg-next" ${page + 1 >= totalPages ? 'disabled' : ''}>下一页</button>
          </div>` : ''}
      `;

      const applyFilters = () => {
        navigate(buildURL({
          q: document.getElementById('f-q').value.trim(),
          sort: document.getElementById('f-sort').value,
          read_status: document.getElementById('f-read').value,
          scrape_status: document.getElementById('f-scrape').value,
          oneshot: document.getElementById('f-oneshot').value,
          status: document.getElementById('f-status').value,
          genre: document.getElementById('f-genre').value,
          tag: document.getElementById('f-tag').value,
          publisher: document.getElementById('f-pub').value,
          author: document.getElementById('f-author').value.trim(),
        }));
      };
      document.getElementById('f-apply').onclick = applyFilters;
      document.getElementById('f-q').addEventListener('keydown', (e) => { if (e.key === 'Enter') applyFilters(); });
      document.getElementById('f-author').addEventListener('keydown', (e) => { if (e.key === 'Enter') applyFilters(); });
      ['f-sort', 'f-read', 'f-scrape', 'f-oneshot', 'f-status', 'f-genre', 'f-tag', 'f-pub'].forEach((id) => {
        document.getElementById(id).onchange = applyFilters;
      });
      document.getElementById('f-reset').onclick = () => {
        navigate(libraryID ? `/libraries/${encodeURIComponent(libraryID)}` : '/libraries');
      };
      if (totalPages > 1) {
        document.getElementById('pg-prev').onclick = () => navigate(buildURL({ page: page - 1 }));
        document.getElementById('pg-next').onclick = () => navigate(buildURL({ page: page + 1 }));
      }
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  // --- Series Detail View (/series/:id) ---

  function pickStartBook(books) {
    if (!books || books.length === 0) return null;
    const bookLabel = (b) => (b.metadata && b.metadata.title) ? b.metadata.title : b.name;
    const inProg = books.find((b) => b.readProgress && !b.readProgress.completed && b.readProgress.page > 0);
    if (inProg) {
      const mid = bookLabel(inProg);
      const page = inProg.readProgress.page;
      return {
        book: inProg,
        page,
        prefix: '继续阅读 ·\u00a0',
        middle: mid,
        suffix: `（第 ${page} 页）`,
        fullLabel: `继续阅读 · ${mid}（第 ${page} 页）`,
      };
    }
    const firstUnread = books.find((b) => !b.readProgress || !b.readProgress.completed);
    if (firstUnread) {
      const hasAnyRead = books.some((b) => b.readProgress && b.readProgress.completed);
      if (hasAnyRead) {
        const mid = bookLabel(firstUnread);
        return {
          book: firstUnread,
          page: 1,
          prefix: '继续读下一卷 ·\u00a0',
          middle: mid,
          suffix: '',
          fullLabel: `继续读下一卷 · ${mid}`,
        };
      }
      return {
        book: firstUnread,
        page: 1,
        prefix: '开始阅读',
        middle: '',
        suffix: '',
        fullLabel: '开始阅读',
      };
    }
    return {
      book: books[0],
      page: 1,
      prefix: '从第一卷重读',
      middle: '',
      suffix: '',
      fullLabel: '从第一卷重读',
    };
  }

  async function renderSeriesDetail(seriesID) {
    renderShell('libraries', `<div class="boot-splash" style="min-height:45vh"><div class="spinner"></div><p>正在读取作品详情…</p></div>`);
    try {
      const [detail, maintJobs] = await Promise.all([
        api(`/admin/api/series/${encodeURIComponent(seriesID)}/detail`),
        api(`/admin/api/maintenance-jobs?targetType=series&targetId=${encodeURIComponent(seriesID)}&limit=6`),
      ]);
      paintSeriesDetail(seriesID, detail, maintJobs || []);
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  function paintSeriesDetail(seriesID, detail, maintJobs) {
    const s = detail.series;
    const meta = s.metadata || {};
    const rawMeta = detail.rawMeta || {};
    const books = detail.books || [];
    const extras = detail.bookExtras || {};
    const lib = detail.library || {};
    const title = meta.title || detail.originalName;
    document.title = `${title} · fake-komga-115`;

    const startTarget = pickStartBook(books);
    const coverSrc = rawMeta.coverSource || 'auto';
    const bgmID = rawMeta.bgmSubjectId || 0;
    const bgmScore = typeof rawMeta.bgmScore === 'number' && rawMeta.bgmScore > 0 ? rawMeta.bgmScore.toFixed(1) : '';
    const authors = (rawMeta.authors || []);
    const genres = (meta.genres || []);
    const tags = (meta.tags || []);
    const altTitles = (meta.alternateTitles || []);

    const content = document.getElementById('page-content');
    if (!content) return;

    content.innerHTML = `
      <div style="margin-bottom:12px;font-size:13px;color:var(--text-muted);overflow-wrap:anywhere;word-break:break-word">
        <a href="/libraries/${encodeURIComponent(lib.id || '')}" data-nav style="color:var(--accent)">${esc(lib.name || '书库')}</a>
        <span> / </span>
        <span>${esc(title)}</span>
      </div>

      <section class="detail-hero">
        <div class="detail-cover-col">
          <div class="detail-cover-frame">
            <img id="series-cover-img" src="/api/v1/series/${encodeURIComponent(seriesID)}/thumbnail?v=${state.coverVer}" alt="${esc(title)}">
          </div>
          <div class="segmented" title="切换封面显示来源">
            <button type="button" data-cover-src="auto" class="${coverSrc === 'auto' ? 'active' : ''}">自动</button>
            <button type="button" data-cover-src="bangumi" class="${coverSrc === 'bangumi' ? 'active' : ''}">Bangumi</button>
            <button type="button" data-cover-src="book" class="${coverSrc === 'book' ? 'active' : ''}">首卷图</button>
          </div>
        </div>

        <div class="detail-main">
          <div class="detail-badges">
            <span class="badge">${esc(providerLabel(lib.providerType))}</span>
            <span class="badge">${s.oneshot ? '单本 One-Shot' : `共 ${books.length} 册 · ${esc(detail.totalSize)}`}</span>
            <span class="badge badge-accent">${esc(meta.status || 'ONGOING')}</span>
            ${rawMeta.isNsfw ? `<span class="badge badge-danger">NSFW</span>` : ''}
            ${meta.publisher ? `<a href="/libraries?publisher=${encodeURIComponent(meta.publisher)}" data-nav class="badge">${esc(meta.publisher)}</a>` : ''}
            ${bgmID > 0
              ? `<a href="https://bgm.tv/subject/${bgmID}" target="_blank" rel="noopener noreferrer" class="badge badge-bgm" title="若打开提示“数据库中没有查询您所指定的条目”，说明该条目在 Bangumi 网页端受登录保护，请先在浏览器登录 bgm.tv 账号后再访问">Bangumi #${bgmID}${bgmScore ? ` · ★ ${bgmScore}` : ''} ↗</a>`
              : `<span class="badge">${rawMeta.scrapeStatus === 'unmatched' ? 'Bangumi 未命中' : '尚未匹配 Bangumi'}</span>`}
          </div>

          <h1>${esc(title)}</h1>
          <div class="detail-orig">
            ${detail.originalName !== title ? `<div>原始目录名：<strong>${esc(detail.originalName)}</strong></div>` : ''}
            <div>相对路径：${esc(detail.relativePath)}</div>
            ${bgmID > 0 ? `<div style="margin-top:2px">提示：部分带受限标签的条目在未登录 <code>bgm.tv</code> 时网页会提示“没有查询您所指定的条目”，登录 Bangumi 账号后即可正常打开。</div>` : ''}
          </div>

          <div class="detail-cta">
            ${startTarget ? `<a href="/reader/series/${encodeURIComponent(seriesID)}/book/${encodeURIComponent(startTarget.book.id)}?page=${startTarget.page}" class="btn btn-primary btn-lg btn-cta-read" title="${esc(startTarget.fullLabel)}"><span class="btn-part-fixed">▶\u00a0${esc(startTarget.prefix)}</span>${startTarget.middle ? `<span class="btn-part-ellipsis">${esc(startTarget.middle)}</span>` : ''}${startTarget.suffix ? `<span class="btn-part-fixed">${esc(startTarget.suffix)}</span>` : ''}</a>` : ''}
            <button type="button" class="btn btn-bgm" id="btn-open-bgm">Bangumi 刮削 / 匹配</button>
            <button type="button" class="btn" id="btn-edit-meta">编辑元数据</button>
            <button type="button" class="btn btn-sm" id="btn-mark-series-read">全部标为已读</button>
            <button type="button" class="btn btn-sm" id="btn-reset-series-read">重置未读</button>
          </div>

          ${meta.summary ? `<p class="detail-summary">${esc(meta.summary)}</p>` : ''}

          ${authors.length > 0 ? `
            <div class="chip-group">
              <span style="font-size:12px;color:var(--text-muted)">作者：</span>
              ${authors.map((a) => `<a href="/libraries?author=${encodeURIComponent(a.name)}" data-nav class="chip">${esc(a.name)}${a.role && a.role !== 'writer' ? ` (${esc(a.role)})` : ''}</a>`).join('')}
            </div>` : ''}

          ${genres.length > 0 ? `
            <div class="chip-group">
              <span style="font-size:12px;color:var(--text-muted)">分类：</span>
              ${genres.map((g) => `<a href="/libraries?genre=${encodeURIComponent(g)}" data-nav class="chip">${esc(g)}</a>`).join('')}
            </div>` : ''}

          ${tags.length > 0 ? `
            <div class="chip-group">
              <span style="font-size:12px;color:var(--text-muted)">标签：</span>
              ${tags.map((t) => `<a href="/libraries?tag=${encodeURIComponent(t)}" data-nav class="chip"># ${esc(t)}</a>`).join('')}
            </div>` : ''}

          ${altTitles.length > 0 ? `
            <div style="font-size:12px;color:var(--text-muted);margin-top:8px">
              别名：${altTitles.map((at) => esc(at.title)).join(' / ')}
            </div>` : ''}
        </div>
      </section>

      <details class="card" id="series-maint-panel" ${maintJobs.some((j) => j.status === 'queued' || j.status === 'running') ? 'open' : ''}>
        <summary style="cursor:pointer;font-weight:600;user-select:none">归档索引、推断阅读进度与真实下载统计（Mihon / 维护面板）</summary>
        <div style="margin-top:14px;display:grid;gap:10px;font-size:13px">
          <div><strong>Mihon / Komga 同步进度：</strong>已读 ${s.booksReadCount} / ${s.booksCount} 本${s.booksInProgressCount > 0 ? `（阅读中 ${s.booksInProgressCount} 本）` : ''}</div>
          ${detail.inferred ? `<div><strong>页面流推断进度：</strong>最近 ${esc(detail.inferred.last)} · 最大 ${esc(detail.inferred.max)}</div>` : ''}
          <div><strong>归档索引统计：</strong>${esc(detail.indexSummary)}</div>
          <div><strong>系列封面统计：</strong>${esc(detail.coverSummary)}</div>
          <div><strong>真实 Range 下载统计：</strong>${esc(detail.downloadStat)}</div>
          <div style="display:flex;flex-wrap:wrap;gap:8px;margin-top:6px">
            <button type="button" class="btn btn-sm" data-maint="series_index" data-force="false">生成全部缺失索引</button>
            <button type="button" class="btn btn-sm" data-maint="series_index" data-force="true">强制重建全部索引</button>
            <button type="button" class="btn btn-sm" data-maint="series_thumbnail" data-force="false">生成本地首卷封面</button>
            <button type="button" class="btn btn-sm" data-maint="series_thumbnail" data-force="true">强制重建本地封面</button>
          </div>
          <div id="series-maint-jobs">${renderMaintJobRows(maintJobs)}</div>
        </div>
      </details>

      <section class="card">
        <div class="section-head" style="margin-bottom:12px">
          <h2>卷列表（${books.length}）</h2>
        </div>
        <div class="volume-list">
          ${books.map((b) => {
            const ex = extras[b.id] || {};
            const rp = b.readProgress;
            const pgCount = (b.media && b.media.pagesCount) || 0;
            const startPg = rp && !rp.completed && rp.page > 0 ? rp.page : 1;
            let stateBadge = `<span class="badge">未读</span>`;
            if (rp && rp.completed) stateBadge = `<span class="badge badge-success">已读</span>`;
            else if (rp && rp.page > 0) stateBadge = `<span class="badge badge-accent">第 ${rp.page}${pgCount > 0 ? ` / ${pgCount}` : ''} 页</span>`;
            return `
              <div class="volume-row">
                <div class="volume-info">
                  <div class="volume-title">
                    <a href="/book/${encodeURIComponent(b.id)}" data-nav>${esc(b.name)}</a>
                  </div>
                  <div class="volume-meta">
                    ${stateBadge}
                    <span>${esc(b.size)}</span>
                    <span>${pgCount > 0 ? `${pgCount} 页` : '未建页数索引'}</span>
                    ${ex.indexStatus ? `<span>${esc(ex.indexStatus)}</span>` : ''}
                    ${ex.downloadStat ? `<span>${esc(ex.downloadStat)}</span>` : ''}
                    ${ex.inferredProgress ? `<span>${esc(ex.inferredProgress)}</span>` : ''}
                  </div>
                </div>
                <div class="volume-actions">
                  <a href="/reader/series/${encodeURIComponent(seriesID)}/book/${encodeURIComponent(b.id)}?page=${startPg}" class="btn btn-primary btn-sm">${rp && !rp.completed && rp.page > 0 ? '继续读' : '阅读'}</a>
                  <a href="/book/${encodeURIComponent(b.id)}" data-nav class="btn btn-sm">详情</a>
                  ${rp && rp.completed
                    ? `<button type="button" class="btn btn-sm" data-book-unread="${esc(b.id)}">标未读</button>`
                    : `<button type="button" class="btn btn-sm" data-book-read="${esc(b.id)}">标已读</button>`}
                </div>
              </div>`;
          }).join('')}
        </div>
      </section>
    `;

    content.querySelectorAll('button[data-cover-src]').forEach((btn) => {
      btn.onclick = async () => {
        const src = btn.getAttribute('data-cover-src');
        try {
          await api(`/api/v1/series/${encodeURIComponent(seriesID)}/metadata`, {
            method: 'PATCH',
            body: { coverSource: src },
          });
          state.coverVer = Date.now();
          toast('封面来源已切换');
          renderSeriesDetail(seriesID);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    document.getElementById('btn-open-bgm').onclick = () => openBangumiMatchModal(seriesID, detail);
    document.getElementById('btn-edit-meta').onclick = () => openMetadataEditModal(seriesID, detail);

    document.getElementById('btn-mark-series-read').onclick = async () => {
      try {
        await api(`/api/v1/series/${encodeURIComponent(seriesID)}/read-progress`, { method: 'POST' });
        toast('已将整部作品标记为已读');
        renderSeriesDetail(seriesID);
      } catch (err) { toast(err.message, 'error'); }
    };
    document.getElementById('btn-reset-series-read').onclick = async () => {
      if (!confirm('确认清空整部作品的阅读进度？')) return;
      try {
        await api(`/api/v1/series/${encodeURIComponent(seriesID)}/read-progress`, { method: 'DELETE' });
        toast('阅读进度已重置');
        renderSeriesDetail(seriesID);
      } catch (err) { toast(err.message, 'error'); }
    };

    content.querySelectorAll('button[data-book-read]').forEach((btn) => {
      btn.onclick = async () => {
        const bid = btn.getAttribute('data-book-read');
        try {
          await api(`/api/v1/books/${encodeURIComponent(bid)}/read-progress`, { method: 'PATCH', body: { completed: true } });
          renderSeriesDetail(seriesID);
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    content.querySelectorAll('button[data-book-unread]').forEach((btn) => {
      btn.onclick = async () => {
        const bid = btn.getAttribute('data-book-unread');
        try {
          await api(`/api/v1/books/${encodeURIComponent(bid)}/read-progress`, { method: 'DELETE' });
          renderSeriesDetail(seriesID);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    content.querySelectorAll('button[data-maint]').forEach((btn) => {
      btn.onclick = async () => {
        const operation = btn.getAttribute('data-maint');
        const force = btn.getAttribute('data-force') === 'true';
        try {
          await api('/admin/api/maintenance-jobs', {
            method: 'POST',
            body: { operation, seriesId: seriesID, force },
          });
          toast('维护任务已加入队列');
          pollSeriesMaint(seriesID);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    bindMaintCancelButtons(content, () => pollSeriesMaint(seriesID));
    if (maintJobs.some((j) => j.status === 'queued' || j.status === 'running')) {
      schedulePoll(() => pollSeriesMaint(seriesID), 2000);
    }
  }

  async function pollSeriesMaint(seriesID) {
    if (location.pathname !== `/series/${encodeURIComponent(seriesID)}` && location.pathname !== `/series/${seriesID}`) return;
    try {
      const jobs = await api(`/admin/api/maintenance-jobs?targetType=series&targetId=${encodeURIComponent(seriesID)}&limit=6`);
      const box = document.getElementById('series-maint-jobs');
      if (box) {
        box.innerHTML = renderMaintJobRows(jobs || []);
        bindMaintCancelButtons(box, () => pollSeriesMaint(seriesID));
      }
      if ((jobs || []).some((j) => j.status === 'queued' || j.status === 'running')) {
        schedulePoll(() => pollSeriesMaint(seriesID), 2000);
      } else {
        stopPolling();
        state.coverVer = Date.now();
        const detail = await api(`/admin/api/series/${encodeURIComponent(seriesID)}/detail`);
        paintSeriesDetail(seriesID, detail, jobs || []);
      }
    } catch (_) { stopPolling(); }
  }

  function renderMaintJobRows(jobs) {
    if (!jobs || jobs.length === 0) return '';
    return `<div style="margin-top:8px;display:grid;gap:6px">${jobs.map((j) => `
      <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;padding:7px 10px;border-radius:6px;background:var(--bg-input);border:1px solid var(--border)">
        <span>
          <span class="badge ${j.status === 'completed' ? 'badge-success' : j.status === 'failed' ? 'badge-danger' : 'badge-accent'}">${esc(j.status)}</span>
          <strong style="margin-left:6px">${esc(j.operation)}</strong>
          <span style="color:var(--text-muted);margin-left:8px">${j.processedBooks || 0}/${j.totalBooks || 0}</span>
          ${j.error ? `<span style="color:var(--danger);margin-left:8px">${esc(j.error)}</span>` : ''}
        </span>
        ${(j.status === 'queued' || j.status === 'running') ? `<button type="button" class="btn btn-sm btn-danger" data-cancel-maint="${esc(j.id)}">取消</button>` : ''}
      </div>`).join('')}</div>`;
  }

  function bindMaintCancelButtons(root, onDone) {
    root.querySelectorAll('button[data-cancel-maint]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api(`/admin/api/maintenance-jobs/${encodeURIComponent(btn.getAttribute('data-cancel-maint'))}/cancel`, { method: 'POST' });
          if (onDone) onDone();
        } catch (err) { toast(err.message, 'error'); }
      };
    });
  }

  // --- Bangumi Match Modal ---

  function openBangumiMatchModal(seriesID, detail) {
    const rawMeta = detail.rawMeta || {};
    openModal({
      title: `Bangumi 元数据匹配 · ${detail.originalName}`,
      subheadHTML: `
        <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:8px">
          <input type="text" id="bgm-search-q" value="${esc(detail.originalName)}" placeholder="输入漫画标题关键词、Bangumi 条目 ID 或 bgm.tv 链接" style="flex:1 1 200px;min-width:0;height:38px;padding:0 11px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text)">
          <button type="button" class="btn btn-primary" id="bgm-search-btn">搜索候选</button>
          <button type="button" class="btn btn-bgm" id="bgm-auto-btn">自动清洗匹配</button>
          ${rawMeta.bgmSubjectId ? `<button type="button" class="btn btn-danger" id="bgm-clear-btn">解除绑定 (#${rawMeta.bgmSubjectId})</button>` : ''}
        </div>
        <div id="bgm-parsed-hint" class="bgm-parsed-hint">支持直接粘贴 Subject ID（如 24596）或 https://bgm.tv/subject/... 链接（部分受限条目需登录 bgm.tv 才能在网页端打开）。</div>
      `,
      bodyHTML: `
        <div id="bgm-candidates-box"><div class="boot-splash" style="min-height:140px"><div class="spinner"></div><p>正在搜索 Bangumi…</p></div></div>
      `,
      onMount: () => {
        const searchInput = document.getElementById('bgm-search-q');
        const box = document.getElementById('bgm-candidates-box');
        const hint = document.getElementById('bgm-parsed-hint');
        const defaultHint = hint.textContent;
        let searchID = 0;
        const doSearch = async (qVal) => {
          const id = ++searchID;
          box.innerHTML = `<div class="boot-splash" style="min-height:140px"><div class="spinner"></div><p>正在搜索 Bangumi…</p></div>`;
          try {
            const res = await api(`/admin/api/series/${encodeURIComponent(seriesID)}/bangumi/candidates?q=${encodeURIComponent(qVal)}`);
            if (id !== searchID || !box.isConnected) return;
            const parsed = res.parsed || {};
            hint.textContent = parsed.candidates && parsed.candidates.length > 0
              ? `清洗候选词：${parsed.candidates.join(' / ')}${parsed.authors && parsed.authors.length ? ` · 识别作者：${parsed.authors.join('、')}` : ''}${parsed.publisher ? ` · 版本/出版社：${parsed.publisher}` : ''}`
              : defaultHint;
            hint.title = hint.textContent;
            const cands = res.candidates || [];
            if (cands.length === 0) {
              box.innerHTML = `<div class="empty-box">未找到匹配的 Bangumi 书籍条目，请尝试缩短关键词或直接输入 Bangumi Subject ID。</div>`;
              return;
            }
            box.innerHTML = cands.map((c) => {
              const imgURL = `/admin/api/bangumi/subjects/${encodeURIComponent(c.id)}/cover${c.coverUrl ? `?u=${encodeURIComponent(c.coverUrl)}` : ''}`;
              const authorsTxt = (c.authors || []).map((a) => a.name).join(' / ');
              return `
                <div class="bgm-candidate">
                  <img loading="lazy" src="${esc(imgURL)}" alt="">
                  <div style="flex:1;min-width:0">
                    <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap">
                      <strong style="font-size:15px">${esc(c.nameCn || c.name)}</strong>
                      <span class="badge badge-bgm">#${c.id} · ${esc(c.platformLabel || '漫画')}</span>
                      ${c.nsfw ? `<span class="badge badge-danger">NSFW</span>` : ''}
                      ${c.score ? `<span class="badge">★ ${Number(c.score).toFixed(1)}</span>` : ''}
                      ${c.confidence ? `<span class="badge badge-accent">匹配度 ${Math.round(c.confidence * 100)}%</span>` : ''}
                    </div>
                    ${c.nameCn && c.name && c.name !== c.nameCn ? `<div style="font-size:12px;color:var(--text-muted)">原名：${esc(c.name)}</div>` : ''}
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:3px">
                      ${authorsTxt ? `作者：${esc(authorsTxt)} · ` : ''}
                      ${c.publisher ? `出版社：${esc(c.publisher)} · ` : ''}
                      ${c.releaseDate ? `发售：${esc(c.releaseDate)}` : ''}
                    </div>
                    ${c.summary ? `<div style="font-size:12px;color:var(--text-muted);margin-top:5px;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden">${esc(c.summary)}</div>` : ''}
                  </div>
                  <div style="display:flex;flex-direction:column;gap:6px;justify-content:center">
                    <button type="button" class="btn btn-primary btn-sm" data-bind-bgm="${c.id}">选用此条目</button>
                    <a href="https://bgm.tv/subject/${c.id}" target="_blank" rel="noopener noreferrer" class="btn btn-sm">查看 ↗</a>
                  </div>
                </div>`;
            }).join('');

            box.querySelectorAll('button[data-bind-bgm]').forEach((btn) => {
              btn.onclick = async () => {
                const subjectId = Number(btn.getAttribute('data-bind-bgm'));
                btn.disabled = true;
                try {
                  await api(`/admin/api/series/${encodeURIComponent(seriesID)}/bangumi/bind`, {
                    method: 'POST',
                    body: { subjectId },
                  });
                  state.coverVer = Date.now();
                  state.taxonomies.loaded = false;
                  toast(`已绑定 Bangumi #${subjectId}`);
                  closeModal();
                  renderSeriesDetail(seriesID);
                } catch (err) {
                  toast(err.message, 'error');
                  btn.disabled = false;
                }
              };
            });
          } catch (err) {
            if (id !== searchID || !box.isConnected) return;
            box.innerHTML = `<div class="empty-box" style="color:var(--danger)">${esc(err.message)}</div>`;
          }
        };

        document.getElementById('bgm-search-btn').onclick = () => doSearch(searchInput.value.trim());
        searchInput.addEventListener('keydown', (e) => {
          if (e.key !== 'Enter' || e.isComposing || e.keyCode === 229) return;
          e.preventDefault();
          doSearch(searchInput.value.trim());
        });
        document.getElementById('bgm-auto-btn').onclick = async () => {
          try {
            const res = await api(`/admin/api/series/${encodeURIComponent(seriesID)}/bangumi/auto`, { method: 'POST', body: {} });
            state.coverVer = Date.now();
            state.taxonomies.loaded = false;
            if (res.matched) {
              toast('自动匹配成功！');
              closeModal();
              renderSeriesDetail(seriesID);
            } else {
              toast('自动匹配置信度不足，请从候选列表中手动选择', 'error');
            }
          } catch (err) { toast(err.message, 'error'); }
        };
        const clearBtn = document.getElementById('bgm-clear-btn');
        if (clearBtn) {
          clearBtn.onclick = async () => {
            if (!confirm('确认解除 Bangumi 绑定并恢复原始目录标题与首卷封面？')) return;
            try {
              await api(`/admin/api/series/${encodeURIComponent(seriesID)}/bangumi/bind`, { method: 'DELETE' });
              state.coverVer = Date.now();
              toast('已解除 Bangumi 绑定');
              closeModal();
              renderSeriesDetail(seriesID);
            } catch (err) { toast(err.message, 'error'); }
          };
        }
        doSearch('');
      },
    });
  }

  // --- Metadata Edit Modal ---

  function openMetadataEditModal(seriesID, detail) {
    const m = detail.rawMeta || {};
    const genresStr = (m.genres || []).join(', ');
    const tagsStr = (m.tags || []).join(', ');
    openModal({
      title: '编辑系列元数据（手动保存后自动锁定对应字段）',
      bodyHTML: `
        <div class="grid-2">
          <div class="field">
            <label>显示标题 <label class="check-row" style="float:right"><input type="checkbox" id="em-title-lock" ${m.titleLock ? 'checked' : ''}>锁定</label></label>
            <input type="text" id="em-title" value="${esc(m.title || detail.originalName)}">
          </div>
          <div class="field">
            <label>排序标题 <label class="check-row" style="float:right"><input type="checkbox" id="em-sort-lock" ${m.titleSortLock ? 'checked' : ''}>锁定</label></label>
            <input type="text" id="em-sort" value="${esc(m.titleSort || m.title || detail.originalName)}">
          </div>
          <div class="field">
            <label>连载状态 <label class="check-row" style="float:right"><input type="checkbox" id="em-status-lock" ${m.statusLock ? 'checked' : ''}>锁定</label></label>
            <select id="em-status">
              <option value="ONGOING" ${m.status === 'ONGOING' ? 'selected' : ''}>ONGOING（连载中）</option>
              <option value="ENDED" ${m.status === 'ENDED' ? 'selected' : ''}>ENDED（已完结）</option>
              <option value="HIATUS" ${m.status === 'HIATUS' ? 'selected' : ''}>HIATUS（休刊）</option>
              <option value="ABANDONED" ${m.status === 'ABANDONED' ? 'selected' : ''}>ABANDONED（中止）</option>
            </select>
          </div>
          <div class="field">
            <label>阅读方向 <label class="check-row" style="float:right"><input type="checkbox" id="em-dir-lock" ${m.readingDirectionLock ? 'checked' : ''}>锁定</label></label>
            <select id="em-dir">
              <option value="RIGHT_TO_LEFT" ${m.readingDirection === 'RIGHT_TO_LEFT' ? 'selected' : ''}>右翻日漫 (RIGHT_TO_LEFT)</option>
              <option value="LEFT_TO_RIGHT" ${m.readingDirection === 'LEFT_TO_RIGHT' ? 'selected' : ''}>左翻国漫/美漫 (LEFT_TO_RIGHT)</option>
              <option value="VERTICAL" ${m.readingDirection === 'VERTICAL' ? 'selected' : ''}>上下条漫 (VERTICAL)</option>
            </select>
          </div>
          <div class="field">
            <label>出版社 <label class="check-row" style="float:right"><input type="checkbox" id="em-pub-lock" ${m.publisherLock ? 'checked' : ''}>锁定</label></label>
            <input type="text" id="em-pub" value="${esc(m.publisher || '')}">
          </div>
          <div class="field">
            <label>发售日期 <label class="check-row" style="float:right"><input type="checkbox" id="em-date-lock" ${m.releaseDateLock ? 'checked' : ''}>锁定</label></label>
            <input type="text" id="em-date" placeholder="YYYY-MM-DD" value="${esc(m.releaseDate || '')}">
          </div>
          <div class="field">
            <label>NSFW 分级标记 <label class="check-row" style="float:right"><input type="checkbox" id="em-nsfw-lock" ${m.nsfwLock ? 'checked' : ''}>锁定</label></label>
            <select id="em-nsfw">
              <option value="false" ${!m.isNsfw ? 'selected' : ''}>普通作品（非 NSFW）</option>
              <option value="true" ${m.isNsfw ? 'selected' : ''}>NSFW（受后台显示开关控制）</option>
            </select>
          </div>
        </div>
        <div class="field">
          <label>分类 Genres（英文逗号分隔） <label class="check-row" style="float:right"><input type="checkbox" id="em-genres-lock" ${m.genresLock ? 'checked' : ''}>锁定</label></label>
          <input type="text" id="em-genres" value="${esc(genresStr)}">
        </div>
        <div class="field">
          <label>标签 Tags（英文逗号分隔） <label class="check-row" style="float:right"><input type="checkbox" id="em-tags-lock" ${m.tagsLock ? 'checked' : ''}>锁定</label></label>
          <input type="text" id="em-tags" value="${esc(tagsStr)}">
        </div>
        <div class="field">
          <label>简介 Summary <label class="check-row" style="float:right"><input type="checkbox" id="em-sum-lock" ${m.summaryLock ? 'checked' : ''}>锁定</label></label>
          <textarea id="em-sum">${esc(m.summary || '')}</textarea>
        </div>
      `,
      footHTML: `
        <button type="button" class="btn" id="em-unlock-all">全部解除锁定</button>
        <button type="button" class="btn btn-primary" id="em-save">保存元数据</button>
      `,
      onMount: () => {
        document.getElementById('em-unlock-all').onclick = () => {
          ['em-title-lock', 'em-sort-lock', 'em-status-lock', 'em-dir-lock', 'em-pub-lock', 'em-date-lock', 'em-nsfw-lock', 'em-genres-lock', 'em-tags-lock', 'em-sum-lock']
            .forEach((id) => { document.getElementById(id).checked = false; });
        };
        document.getElementById('em-save').onclick = async () => {
          const splitCSV = (s) => s.split(',').map((x) => x.trim()).filter(Boolean);
          const patch = {
            title: document.getElementById('em-title').value.trim(),
            titleLock: document.getElementById('em-title-lock').checked,
            titleSort: document.getElementById('em-sort').value.trim(),
            titleSortLock: document.getElementById('em-sort-lock').checked,
            status: document.getElementById('em-status').value,
            statusLock: document.getElementById('em-status-lock').checked,
            readingDirection: document.getElementById('em-dir').value,
            readingDirectionLock: document.getElementById('em-dir-lock').checked,
            publisher: document.getElementById('em-pub').value.trim(),
            publisherLock: document.getElementById('em-pub-lock').checked,
            releaseDate: document.getElementById('em-date').value.trim(),
            releaseDateLock: document.getElementById('em-date-lock').checked,
            isNsfw: document.getElementById('em-nsfw').value === 'true',
            nsfwLock: document.getElementById('em-nsfw-lock').checked,
            genres: splitCSV(document.getElementById('em-genres').value),
            genresLock: document.getElementById('em-genres-lock').checked,
            tags: splitCSV(document.getElementById('em-tags').value),
            tagsLock: document.getElementById('em-tags-lock').checked,
            summary: document.getElementById('em-sum').value.trim(),
            summaryLock: document.getElementById('em-sum-lock').checked,
          };
          try {
            await api(`/api/v1/series/${encodeURIComponent(seriesID)}/metadata`, {
              method: 'PATCH',
              body: patch,
            });
            state.taxonomies.loaded = false;
            toast('元数据已保存');
            closeModal();
            renderSeriesDetail(seriesID);
          } catch (err) { toast(err.message, 'error'); }
        };
      },
    });
  }

  // --- Book Detail View (/book/:id & /books/:id) ---

  async function renderBookDetail(bookID) {
    renderShell('libraries', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在读取单册详情…</p></div>`);
    try {
      const [detail, jobs] = await Promise.all([
        api(`/admin/api/books/${encodeURIComponent(bookID)}/detail`),
        api(`/admin/api/maintenance-jobs?targetType=book&targetId=${encodeURIComponent(bookID)}&limit=6`),
      ]);
      paintBookDetail(bookID, detail, jobs || []);
    } catch (err) { toast(err.message, 'error'); }
  }

  function paintBookDetail(bookID, detail, jobs) {
    const b = detail.book || {};
    const rp = b.readProgress;
    const pgCount = (b.media && b.media.pagesCount) || 0;
    const startPg = rp && !rp.completed && rp.page > 0 ? rp.page : 1;
    document.title = `${b.name || '单册详情'} · fake-komga-115`;

    const content = document.getElementById('page-content');
    if (!content) return;

    content.innerHTML = `
      <div style="margin-bottom:12px;font-size:13px;color:var(--text-muted)">
        <a href="/series/${encodeURIComponent(detail.seriesId)}" data-nav style="color:var(--accent)">← 返回系列：${esc(detail.seriesTitle || detail.originalSeries)}</a>
      </div>

      <section class="card">
        <div class="detail-badges">
          <span class="badge">${esc(providerLabel(detail.providerType))}</span>
          <span class="badge">${esc(detail.libraryName)}</span>
          <span class="badge badge-accent">${esc(detail.archiveType)}</span>
          ${rp && rp.completed
            ? `<span class="badge badge-success">已读完</span>`
            : rp && rp.page > 0
              ? `<span class="badge badge-accent">阅读中 · 第 ${rp.page}${pgCount > 0 ? ` / ${pgCount}` : ''} 页</span>`
              : `<span class="badge">未读</span>`}
        </div>
        <h1 style="margin:6px 0 8px;font-size:22px">${esc(b.name)}</h1>
        <div class="detail-orig">相对路径：${esc(detail.relativePath)}</div>

        <div class="detail-cta" style="margin-top:16px">
          <a href="/reader/series/${encodeURIComponent(detail.seriesId)}/book/${encodeURIComponent(bookID)}?page=${startPg}" class="btn btn-primary btn-lg">
            ▶ ${rp && !rp.completed && rp.page > 0 ? `继续阅读（第 ${rp.page} 页）` : '立即在线阅读'}
          </a>
          ${detail.previousBookId ? `<a href="/book/${encodeURIComponent(detail.previousBookId)}" data-nav class="btn">← 上一卷</a>` : ''}
          ${detail.nextBookId ? `<a href="/book/${encodeURIComponent(detail.nextBookId)}" data-nav class="btn">下一卷 →</a>` : ''}
          <button type="button" class="btn btn-sm" id="bk-mark-read">标记已读</button>
          <button type="button" class="btn btn-sm" id="bk-mark-unread">重置未读</button>
        </div>

        <div class="stat-grid" style="margin-top:18px">
          <div class="stat-box"><div class="k">文件大小</div><div class="v" style="font-size:17px">${esc(b.size)}</div></div>
          <div class="stat-box"><div class="k">页数 / 索引状态</div><div class="v" style="font-size:15px">${esc(detail.indexStatus)}${detail.indexDuration ? ` (${esc(detail.indexDuration)})` : ''}</div></div>
          <div class="stat-box"><div class="k">真实下载速度</div><div class="v" style="font-size:15px">${esc(detail.downloadSpeed)}${detail.downloadBytes ? ` · ${esc(detail.downloadBytes)}` : ''}</div></div>
          ${detail.inferred ? `<div class="stat-box"><div class="k">推断加载进度</div><div class="v" style="font-size:14px">最近 ${esc(detail.inferred.last)}<br>最大 ${esc(detail.inferred.max)}</div></div>` : ''}
        </div>

        <div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:10px">
          <button type="button" class="btn btn-sm" id="bk-idx-build">生成归档索引</button>
          <button type="button" class="btn btn-sm" id="bk-idx-force">强制重建归档索引</button>
        </div>
        <div id="book-maint-jobs">${renderMaintJobRows(jobs)}</div>
      </section>
    `;

    document.getElementById('bk-mark-read').onclick = async () => {
      try {
        await api(`/api/v1/books/${encodeURIComponent(bookID)}/read-progress`, { method: 'PATCH', body: { completed: true } });
        renderBookDetail(bookID);
      } catch (err) { toast(err.message, 'error'); }
    };
    document.getElementById('bk-mark-unread').onclick = async () => {
      try {
        await api(`/api/v1/books/${encodeURIComponent(bookID)}/read-progress`, { method: 'DELETE' });
        renderBookDetail(bookID);
      } catch (err) { toast(err.message, 'error'); }
    };
    const startBookIdx = async (force) => {
      try {
        await api('/admin/api/maintenance-jobs', { method: 'POST', body: { operation: 'book_index', bookId: bookID, force } });
        toast('归档索引任务已提交');
        pollBookMaint(bookID);
      } catch (err) { toast(err.message, 'error'); }
    };
    document.getElementById('bk-idx-build').onclick = () => startBookIdx(false);
    document.getElementById('bk-idx-force').onclick = () => startBookIdx(true);
    bindMaintCancelButtons(content, () => pollBookMaint(bookID));
    if (jobs.some((j) => j.status === 'queued' || j.status === 'running')) {
      schedulePoll(() => pollBookMaint(bookID), 1800);
    }
  }

  async function pollBookMaint(bookID) {
    if (!location.pathname.endsWith(`/${encodeURIComponent(bookID)}`) && !location.pathname.endsWith(`/${bookID}`)) return;
    try {
      const jobs = await api(`/admin/api/maintenance-jobs?targetType=book&targetId=${encodeURIComponent(bookID)}&limit=6`);
      const box = document.getElementById('book-maint-jobs');
      if (box) {
        box.innerHTML = renderMaintJobRows(jobs || []);
        bindMaintCancelButtons(box, () => pollBookMaint(bookID));
      }
      if ((jobs || []).some((j) => j.status === 'queued' || j.status === 'running')) {
        schedulePoll(() => pollBookMaint(bookID), 1800);
      } else {
        stopPolling();
        const detail = await api(`/admin/api/books/${encodeURIComponent(bookID)}/detail`);
        paintBookDetail(bookID, detail, jobs || []);
      }
    } catch (_) { stopPolling(); }
  }

  // --- Admin Console (/admin) ---

  async function renderAdmin() {
    const sp = new URLSearchParams(location.search);
    const tab = sp.get('tab') || state.adminTab || 'overview';
    state.adminTab = tab;
    document.title = '管理后台 · fake-komga-115';

    const tabs = [
      ['overview', '概览与连接'],
      ['sources', '存储源（115 / WebDAV / 本地·SMB）'],
      ['libraries', '书库管理'],
      ['bangumi', 'Bangumi 刮削中心'],
      ['jobs', '后台任务'],
      ['settings', '缓存与设置'],
      ['security', '账号与 API Key'],
    ];

    renderShell('admin', `
      <div class="admin-tabs">
        ${tabs.map(([id, label]) => `<button type="button" data-admin-tab="${id}" class="${tab === id ? 'active' : ''}">${esc(label)}</button>`).join('')}
      </div>
      <div id="admin-tab-body"><div class="boot-splash" style="min-height:35vh"><div class="spinner"></div></div></div>
    `);

    document.querySelectorAll('button[data-admin-tab]').forEach((btn) => {
      btn.onclick = () => {
        const next = btn.getAttribute('data-admin-tab');
        state.adminTab = next;
        navigate(`/admin?tab=${encodeURIComponent(next)}`, true);
      };
    });

    const body = document.getElementById('admin-tab-body');
    if (!body) return;

    switch (tab) {
      case 'sources': await renderAdminSources(body); break;
      case 'libraries': await renderAdminLibraries(body); break;
      case 'bangumi': await renderAdminBangumi(body); break;
      case 'jobs': await renderAdminJobs(body); break;
      case 'settings': await renderAdminSettings(body); break;
      case 'security': await renderAdminSecurity(body); break;
      default: await renderAdminOverview(body); break;
    }
  }

  async function renderAdminOverview(body) {
    const st = await api('/admin/api/status');
    const perf = st.performance || {};
    const addrs = st.mihonAddresses || [];
    body.innerHTML = `
      <div class="stat-grid">
        <div class="stat-box"><div class="k">有效书库数</div><div class="v">${st.libraries}${st.hiddenLibraries > 0 ? `<span style="font-size:12px;font-weight:500;color:var(--text-muted);margin-left:6px">（已隐藏 ${st.hiddenLibraries}）</span>` : ''}</div></div>
        <div class="stat-box"><div class="k">漫画系列总数</div><div class="v">${Number(st.series || 0).toLocaleString()}${(!st.showNsfw && st.nsfwSeries > 0) ? `<span style="font-size:12px;font-weight:500;color:var(--text-muted);margin-left:6px">（已隐藏 NSFW ${Number(st.nsfwSeries).toLocaleString()} 部）</span>` : ''}</div></div>
        <div class="stat-box"><div class="k">漫画文件总数</div><div class="v">${Number(st.books || 0).toLocaleString()}</div></div>
        <div class="stat-box"><div class="k">漫画总容量</div><div class="v">${fmtBytes(st.comicBytes)}</div></div>
        <div class="stat-box"><div class="k">Bangumi 已匹配</div><div class="v" style="color:var(--bgm)">${Number(st.bgmMatched || 0).toLocaleString()}</div></div>
        <div class="stat-box"><div class="k">115 / WebDAV 状态</div><div class="v" style="font-size:15px">${st.configured115 ? '115 已连接' : '115 未配'} · WebDAV ${st.webdavAccounts || 0} 个</div></div>
      </div>

      <div class="grid-2">
        <div class="card">
          <h3>Mihon / Komga 客户端接入地址</h3>
          <p style="color:var(--text-secondary);font-size:13px">在 Mihon 的 Komga 扩展中填入以下任一局域网地址，并配置管理员账号密码（HTTP Basic）或在「账号与 API Key」页签生成专属 API Key：</p>
          <div style="display:grid;gap:8px;margin-top:10px">
            ${addrs.map((u) => `<div class="code-box">${esc(u)}</div>`).join('') || `<div class="code-box">${esc(st.adminUrl || location.origin)}</div>`}
          </div>
          <div style="margin-top:12px;font-size:12.5px;color:var(--text-muted)">
            版本：<code>${esc(st.version)}</code> · 数据目录：<code>${esc(st.dataDir)}</code>
          </div>
        </div>

        <div class="card">
          <h3>全局索引与流式读取统计</h3>
          <div style="display:grid;gap:8px;font-size:13px;color:var(--text-secondary)">
            <div>已建立归档索引单册：<strong>${Number(perf.indexCount ?? perf.indexedBooksCount ?? 0).toLocaleString()}</strong></div>
            <div>累计真实 Range 读取流量：<strong>${fmtBytes(perf.downloadBytes || 0)}</strong>（${Number(perf.downloadSamples || 0).toLocaleString()} 次请求）</div>
          </div>
        </div>
      </div>
    `;
  }

  async function renderAdminSources(body) {
    const [acc115, webdavs] = await Promise.all([
      api('/admin/api/account'),
      api('/admin/api/webdav-accounts'),
    ]);
    body.innerHTML = `
      <div class="grid-2">
        <div class="card">
          <h3>115 开放平台令牌</h3>
          <p style="font-size:12.5px;color:var(--text-muted)">状态：RefreshToken ${acc115.hasRefreshToken ? '✅ 已保存' : '❌ 未配置'} · AccessToken ${acc115.hasAccessToken ? '✅ 已保存' : '未缓存'} · 更新于 ${fmtTime(acc115.updatedAt)}</p>
          <details class="field-hint"${acc115.hasRefreshToken ? '' : ' open'}>
            <summary>如何获取 115 Token？</summary>
            <ol>
              <li>打开 <a href="https://api.oplist.org/" target="_blank" rel="noopener noreferrer">OpenList 授权工具 ↗</a>，选择 115 网盘的 OAuth2 授权登录。</li>
              <li>勾选「使用 OpenList 提供的参数」，点击获取 Token，再在弹出的窗口登录 115 并确认授权。</li>
              <li>将 <strong>Refresh Token</strong> 和 <strong>Access Token</strong>（可选）粘贴到下方同名字段，点击「保存并验证」。请勿分享或公开截图中的 Token。</li>
            </ol>
          </details>
          <div class="field">
            <label>Refresh Token（留空则保留现有）</label>
            <input type="password" id="s115-refresh" placeholder="输入新的 115 Refresh Token">
          </div>
          <div class="field">
            <label>Access Token（可选，留空自动刷新）</label>
            <input type="password" id="s115-access" placeholder="可选 Access Token">
          </div>
          <div style="display:flex;gap:8px;flex-wrap:wrap">
            <button type="button" class="btn btn-primary" id="s115-save">保存并验证</button>
            <button type="button" class="btn" id="s115-test">测试当前连接</button>
            <button type="button" class="btn" id="s115-force-refresh">强制刷新令牌</button>
          </div>
        </div>

        <div class="card">
          <h3>本地磁盘与 SMB 挂载目录说明</h3>
          <p style="color:var(--text-secondary);font-size:13px">
            本地目录存储源无需预配账号，支持服务器本地磁盘、Linux 挂载的 SMB/CIFS 共享（如 <code>/share/manga</code>、<code>/mnt/smb</code>）以及 Windows 映射网络驱动器（如 <code>Z:\</code> 或 <code>\\nas\manga</code>）。
          </p>
          <ul style="color:var(--text-muted);font-size:12.5px;padding-left:18px;line-height:1.7">
            <li>通过内核 <code>io.ReaderAt</code> 原生按偏移量随机读取，自动跳过分块磁盘缓存，零双份空间浪费，并针对 SMB 目录遍历去除了多余元数据往返。</li>
            <li>内置<strong>空挂载点防误删保护</strong>：当已有漫画数据的本地/SMB 根目录因断连变为空目录时，自动中止扫描并保留全部历史系列、元数据与阅读进度。</li>
            <li>Docker 部署时可将宿主机目录或 SMB 挂载点通过 <code>-v /share/manga:/manga:ro</code> 只读映射进容器。</li>
          </ul>
        </div>
      </div>

      <div class="card">
        <div class="section-head">
          <h3>WebDAV 账号列表（支持 OpenList / CloudDrive2 / 群晖 / Nextcloud 等）</h3>
          <button type="button" class="btn btn-primary btn-sm" id="wd-add-btn">+ 新增 WebDAV 账号</button>
        </div>
        ${(webdavs || []).length === 0
          ? `<div class="empty-box">暂未添加 WebDAV 账号。</div>`
          : `<div class="table-wrap"><table class="data-table">
              <thead><tr><th>名称</th><th>服务器地址</th><th>用户名</th><th>限速 (req/s)</th><th>TLS</th><th>操作</th></tr></thead>
              <tbody>
                ${webdavs.map((w) => `
                  <tr>
                    <td><strong>${esc(w.name)}</strong></td>
                    <td><code>${esc(w.baseUrl)}</code></td>
                    <td>${esc(w.username || '—')}</td>
                    <td>${w.ratePerSecond || 8}</td>
                    <td>${w.insecureTls ? '<span class="badge badge-warn">跳过证书校验</span>' : '标准验证'}</td>
                    <td style="display:flex;gap:6px;flex-wrap:wrap">
                      <button type="button" class="btn btn-sm" data-wd-test="${esc(w.id)}">测试</button>
                      <button type="button" class="btn btn-sm" data-wd-edit='${esc(JSON.stringify(w))}'>编辑</button>
                      <button type="button" class="btn btn-sm btn-danger" data-wd-del="${esc(w.id)}">删除</button>
                    </td>
                  </tr>`).join('')}
              </tbody>
            </table></div>`}
      </div>
    `;

    document.getElementById('s115-save').onclick = async () => {
      try {
        await api('/admin/api/account', {
          method: 'PUT',
          body: {
            refreshToken: document.getElementById('s115-refresh').value.trim(),
            accessToken: document.getElementById('s115-access').value.trim(),
          },
        });
        toast('115 账号保存并验证成功');
        renderAdminSources(body);
      } catch (err) { toast(err.message, 'error'); }
    };
    document.getElementById('s115-test').onclick = async () => {
      try { await api('/admin/api/account/test', { method: 'POST' }); toast('115 连接正常'); }
      catch (err) { toast(err.message, 'error'); }
    };
    document.getElementById('s115-force-refresh').onclick = async () => {
      try { await api('/admin/api/account/refresh', { method: 'POST' }); toast('115 令牌刷新成功'); renderAdminSources(body); }
      catch (err) { toast(err.message, 'error'); }
    };

    document.getElementById('wd-add-btn').onclick = () => openWebDAVModal(null, () => renderAdminSources(body));
    body.querySelectorAll('button[data-wd-test]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api('/admin/api/webdav-accounts/test', { method: 'POST', body: { id: btn.getAttribute('data-wd-test') } });
          toast('WebDAV 连接测试通过');
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-wd-edit]').forEach((btn) => {
      btn.onclick = () => openWebDAVModal(JSON.parse(btn.getAttribute('data-wd-edit')), () => renderAdminSources(body));
    });
    body.querySelectorAll('button[data-wd-del]').forEach((btn) => {
      btn.onclick = async () => {
        if (!confirm('确认删除该 WebDAV 账号？（若仍有书库使用该账号将拒绝删除）')) return;
        try {
          await api(`/admin/api/webdav-accounts/${encodeURIComponent(btn.getAttribute('data-wd-del'))}`, { method: 'DELETE' });
          toast('已删除 WebDAV 账号');
          renderAdminSources(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });
  }

  function openWebDAVModal(existing, onSaved) {
    const isEdit = Boolean(existing && existing.id);
    openModal({
      title: isEdit ? `编辑 WebDAV 账号 · ${existing.name}` : '新增 WebDAV 账号',
      maxWidth: '540px',
      bodyHTML: `
        <div class="field">
          <label>账号备注名称</label>
          <input type="text" id="wd-name" placeholder="例如：CloudDrive2 / OpenList / 群晖 NAS" value="${esc(existing ? existing.name : '')}">
        </div>
        <div class="field">
          <label>WebDAV 根地址 (Base URL)</label>
          <input type="url" id="wd-url" placeholder="http://127.0.0.1:19798/dav 或 https://nas.example.com:5006/dav" value="${esc(existing ? existing.baseUrl : '')}">
          <div class="field-hint">CloudDrive2 默认 WebDAV 地址通常为 <code>http://&lt;IP&gt;:19798/dav</code>；OpenList 默认通常为 <code>http://&lt;IP&gt;:5244/dav</code>。</div>
        </div>
        <div class="grid-2">
          <div class="field">
            <label>用户名</label>
            <input type="text" id="wd-user" value="${esc(existing ? existing.username : '')}">
          </div>
          <div class="field">
            <label>密码${isEdit ? '（留空保持不变）' : ''}</label>
            <input type="password" id="wd-pass" placeholder="${isEdit ? '留空不修改' : '登录密码'}">
          </div>
        </div>
        <div class="field">
          <label>请求限速（每秒最大请求数，同时限制目录扫描与 Range 读取，默认 8）</label>
          <input type="number" step="0.5" min="0.5" max="100" id="wd-rate" value="${existing && existing.ratePerSecond ? existing.ratePerSecond : 8}">
          <div class="field-hint">若后端通过 CloudDrive2 / OpenList 挂载 115 等网盘，建议设为 <code>2 ~ 4</code> 以避免触发上游风控。</div>
        </div>
        <div class="field">
          <label class="check-row">
            <input type="checkbox" id="wd-insecure" ${existing && existing.insecureTls ? 'checked' : ''}>
            <span>允许局域网自签名 HTTPS 证书（跳过 TLS 校验）</span>
          </label>
        </div>
      `,
      footHTML: `
        <button type="button" class="btn" id="wd-modal-test">测试连接</button>
        <button type="button" class="btn btn-primary" id="wd-modal-save">保存</button>
      `,
      onMount: () => {
        const readForm = () => ({
          id: isEdit ? existing.id : '',
          name: document.getElementById('wd-name').value.trim(),
          baseUrl: document.getElementById('wd-url').value.trim(),
          username: document.getElementById('wd-user').value.trim(),
          password: document.getElementById('wd-pass').value,
          ratePerSecond: parseFloat(document.getElementById('wd-rate').value) || 8,
          insecureTls: document.getElementById('wd-insecure').checked,
        });
        document.getElementById('wd-modal-test').onclick = async () => {
          try {
            await api('/admin/api/webdav-accounts/test', { method: 'POST', body: readForm() });
            toast('WebDAV 连接验证成功！');
          } catch (err) { toast(err.message, 'error'); }
        };
        document.getElementById('wd-modal-save').onclick = async () => {
          const payload = readForm();
          try {
            if (isEdit) {
              await api(`/admin/api/webdav-accounts/${encodeURIComponent(existing.id)}`, { method: 'PUT', body: payload });
            } else {
              await api('/admin/api/webdav-accounts', { method: 'POST', body: payload });
            }
            toast('WebDAV 账号已保存');
            closeModal();
            if (onSaved) onSaved();
          } catch (err) { toast(err.message, 'error'); }
        };
      },
    });
  }

  // --- Admin Libraries Tab ---

  async function renderAdminLibraries(body) {
    const [libs, webdavs] = await Promise.all([
      api('/admin/api/libraries'),
      api('/admin/api/webdav-accounts'),
    ]);
    const allLibs = libs || [];
    const hiddenLibCount = allLibs.filter((l) => !l.enabled).length;
    const activeLibCount = allLibs.length - hiddenLibCount;

    body.innerHTML = `
      <div class="card">
        <h3>新增书库</h3>
        <div class="grid-2">
          <div class="field">
            <label>存储源类型</label>
            <select id="nl-provider">
              <option value="115">115 网盘目录</option>
              <option value="local">服务器本地 / SMB 挂载目录</option>
              <option value="webdav">WebDAV 远程目录（CloudDrive2 / OpenList / NAS）</option>
            </select>
          </div>
          <div class="field">
            <label>书库显示名称</label>
            <input type="text" id="nl-name" placeholder="例如：日漫连载 / 本地珍藏">
          </div>
        </div>

        <div id="nl-115-box">
          <div class="field">
            <label>115 目录 CID <button type="button" class="btn btn-sm" id="nl-browse-115" style="margin-left:8px">浏览 115 文件夹</button></label>
            <input type="text" id="nl-cid" placeholder="输入 115 文件夹 CID 或点击上方浏览选择">
          </div>
        </div>

        <div id="nl-local-box" style="display:none">
          <div class="field">
            <label>服务器本地或 SMB 挂载绝对路径 <button type="button" class="btn btn-sm" id="nl-browse-local" style="margin-left:8px">浏览目录</button></label>
            <input type="text" id="nl-local-path" placeholder="例如：/share/manga、/mnt/smb/comics 或 Z:\\Manga">
          </div>
        </div>

        <div id="nl-webdav-box" style="display:none">
          <div class="grid-2">
            <div class="field">
              <label>选择 WebDAV 账号</label>
              <select id="nl-wd-account">
                ${(webdavs || []).map((w) => `<option value="${esc(w.id)}">${esc(w.name)} (${esc(w.baseUrl)})</option>`).join('') || '<option value="">请先在「存储源」中添加 WebDAV 账号</option>'}
              </select>
            </div>
            <div class="field">
              <label>WebDAV 子目录路径 <button type="button" class="btn btn-sm" id="nl-browse-wd" style="margin-left:8px">浏览远程目录</button></label>
              <input type="text" id="nl-wd-path" value="/" placeholder="/comics">
            </div>
          </div>
        </div>

        <div id="nl-browser-drawer" style="display:none;margin-bottom:14px"></div>

        <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:12px">
          <label class="check-row">
            <input type="checkbox" id="nl-oneshot">
            <span>单本漫画库（One-Shots 模式：每个压缩包独立成为一部作品）</span>
          </label>
          <button type="button" class="btn btn-primary" id="nl-create-btn">创建书库</button>
        </div>
      </div>

      <div class="card">
        <div class="section-head">
          <h3>已配置书库（${allLibs.length}${hiddenLibCount > 0 ? ` · 已隐藏 ${hiddenLibCount}` : ''}）</h3>
          <button type="button" class="btn btn-sm" id="lib-scan-all" ${activeLibCount === 0 ? 'disabled' : ''}>扫描全部未隐藏书库</button>
        </div>
        ${allLibs.length === 0
          ? `<div class="empty-box">暂无书库。</div>`
          : `<div style="display:grid;gap:12px">
              ${allLibs.map((l) => `
                <div style="padding:14px;border-radius:10px;background:var(--bg-elevated);border:1px ${l.enabled ? 'solid' : 'dashed'} var(--border);${l.enabled ? '' : 'opacity:0.74;'}">
                  <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:10px">
                    <div>
                      <strong style="font-size:15.5px">${esc(l.name)}</strong>
                      <span class="badge" style="margin-left:6px">${esc(providerLabel(l.providerType))}</span>
                      ${l.oneShot ? `<span class="badge badge-accent">单本 One-Shots</span>` : ''}
                      ${!l.enabled ? `<span class="badge badge-warn">已隐藏</span>` : ''}
                      <div style="font-size:12px;color:var(--text-muted);margin-top:4px">
                        定位标识：<code>${esc(l.providerType === '115' ? `CID:${l.rootCid}` : l.rootPath || l.rootCid)}</code>
                        · ${Number(l.seriesCount || 0).toLocaleString()} 系列 · ${Number(l.bookCount ?? l.booksCount ?? 0).toLocaleString()} 册 · ${fmtBytes(l.comicBytes)}
                      </div>
                      <div style="font-size:12px;color:var(--text-muted);margin-top:2px">
                        ${!l.enabled ? `<span>已隐藏：保留全部数据与缓存，不参与前台书架、客户端与扫描/刮削等其余操作 · </span>` : ''}最近扫描：${esc(l.lastScanStatus || '从未')} ${l.lastScanCompletedAt ? `(${fmtTime(l.lastScanCompletedAt)})` : ''}
                        ${l.lastScanError ? `<span style="color:var(--danger)"> · ${esc(l.lastScanError)}</span>` : ''}
                      </div>
                    </div>
                    <div style="display:flex;flex-wrap:wrap;gap:6px">
                      ${l.enabled ? `
                        <button type="button" class="btn btn-primary btn-sm" data-lib-scan="${esc(l.id)}">扫描</button>
                        <button type="button" class="btn btn-bgm btn-sm" data-lib-bgm="${esc(l.id)}">刮削 Bangumi</button>
                        <button type="button" class="btn btn-sm" data-lib-cover="${esc(l.id)}">补齐首卷封面</button>
                        <button type="button" class="btn btn-sm" data-lib-toggle-hide="${esc(l.id)}" data-lib-name="${esc(l.name)}" data-lib-next-enabled="false">隐藏</button>
                      ` : `
                        <button type="button" class="btn btn-primary btn-sm" data-lib-toggle-hide="${esc(l.id)}" data-lib-name="${esc(l.name)}" data-lib-next-enabled="true">取消隐藏</button>
                      `}
                      <button type="button" class="btn btn-sm" data-lib-edit='${esc(JSON.stringify(l))}'>编辑</button>
                      <button type="button" class="btn btn-sm btn-danger" data-lib-del="${esc(l.id)}">删除</button>
                    </div>
                  </div>
                </div>`).join('')}
            </div>`}
      </div>
    `;

    const provSel = document.getElementById('nl-provider');
    provSel.onchange = () => {
      const v = provSel.value;
      document.getElementById('nl-115-box').style.display = v === '115' ? '' : 'none';
      document.getElementById('nl-local-box').style.display = v === 'local' ? '' : 'none';
      document.getElementById('nl-webdav-box').style.display = v === 'webdav' ? '' : 'none';
      document.getElementById('nl-browser-drawer').style.display = 'none';
    };

    document.getElementById('nl-browse-115').onclick = () => browse115Drawer('0', ['0']);
    document.getElementById('nl-browse-local').onclick = () => browseLocalDrawer(document.getElementById('nl-local-path').value.trim() || '/');
    document.getElementById('nl-browse-wd').onclick = () => {
      const acc = document.getElementById('nl-wd-account').value;
      if (!acc) { toast('请先选择 WebDAV 账号', 'error'); return; }
      browseWebDAVDrawer(acc, document.getElementById('nl-wd-path').value.trim() || '/');
    };

    document.getElementById('nl-create-btn').onclick = async () => {
      const providerType = provSel.value;
      const name = document.getElementById('nl-name').value.trim();
      const oneShot = document.getElementById('nl-oneshot').checked;
      if (!name) { toast('请输入书库名称', 'error'); return; }
      const payload = { name, providerType, oneShot, enabled: true };
      if (providerType === '115') payload.rootCid = document.getElementById('nl-cid').value.trim();
      else if (providerType === 'local') payload.rootPath = document.getElementById('nl-local-path').value.trim();
      else if (providerType === 'webdav') {
        payload.webdavAccountId = document.getElementById('nl-wd-account').value;
        payload.rootPath = document.getElementById('nl-wd-path').value.trim() || '/';
      }
      try {
        await api('/admin/api/libraries', { method: 'POST', body: payload });
        toast('书库已创建');
        renderAdminLibraries(body);
      } catch (err) { toast(err.message, 'error'); }
    };

    document.getElementById('lib-scan-all').onclick = async () => {
      try {
        await api('/admin/api/scans', { method: 'POST', body: {} });
        toast('已启动全部未隐藏书库扫描');
        navigate('/admin?tab=jobs');
      } catch (err) { toast(err.message, 'error'); }
    };

    body.querySelectorAll('button[data-lib-toggle-hide]').forEach((btn) => {
      btn.onclick = async () => {
        const libID = btn.getAttribute('data-lib-toggle-hide');
        const libName = btn.getAttribute('data-lib-name') || '';
        const nextEnabled = btn.getAttribute('data-lib-next-enabled') === 'true';
        btn.disabled = true;
        try {
          await api(`/admin/api/libraries/${encodeURIComponent(libID)}`, {
            method: 'PUT',
            body: { enabled: nextEnabled },
          });
          toast(nextEnabled
            ? `已恢复显示书库「${libName}」`
            : `已隐藏书库「${libName}」（数据完整保留，相关运行中任务已自动中止）`);
          renderAdminLibraries(body);
        } catch (err) {
          btn.disabled = false;
          toast(err.message, 'error');
        }
      };
    });

    body.querySelectorAll('button[data-lib-scan]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api('/admin/api/scans', { method: 'POST', body: { libraryId: btn.getAttribute('data-lib-scan') } });
          toast('扫描任务已启动');
          navigate('/admin?tab=jobs');
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-lib-bgm]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api('/admin/api/bangumi/runs', { method: 'POST', body: { libraryId: btn.getAttribute('data-lib-bgm'), force: false } });
          toast('已启动本库 Bangumi 批量刮削');
          navigate('/admin?tab=bangumi');
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-lib-cover]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api('/admin/api/cover-jobs', { method: 'POST', body: { libraryId: btn.getAttribute('data-lib-cover'), all: false } });
          toast('封面补齐任务已启动');
          navigate('/admin?tab=jobs');
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-lib-edit]').forEach((btn) => {
      btn.onclick = () => {
        const l = JSON.parse(btn.getAttribute('data-lib-edit'));
        openModal({
          title: `编辑书库 · ${l.name}`,
          maxWidth: '460px',
          bodyHTML: `
            <div class="field"><label>书库名称</label><input type="text" id="el-name" value="${esc(l.name)}"></div>
            <div class="field"><label class="check-row"><input type="checkbox" id="el-hidden" ${!l.enabled ? 'checked' : ''}><span>隐藏此书库（保留全部数据，不参与展示、客户端、扫描与刮削等其余操作）</span></label></div>
            <div class="field"><label class="check-row"><input type="checkbox" id="el-oneshot" ${l.oneShot ? 'checked' : ''}><span>One-Shots 单本漫画库模式（修改后需重扫）</span></label></div>
          `,
          footHTML: `<button type="button" class="btn btn-primary" id="el-save">保存</button>`,
          onMount: () => {
            document.getElementById('el-save').onclick = async () => {
              try {
                await api(`/admin/api/libraries/${encodeURIComponent(l.id)}`, {
                  method: 'PUT',
                  body: {
                    name: document.getElementById('el-name').value.trim(),
                    enabled: !document.getElementById('el-hidden').checked,
                    oneShot: document.getElementById('el-oneshot').checked,
                  },
                });
                toast('书库配置已更新');
                closeModal();
                renderAdminLibraries(body);
              } catch (err) { toast(err.message, 'error'); }
            };
          },
        });
      };
    });
    body.querySelectorAll('button[data-lib-del]').forEach((btn) => {
      btn.onclick = async () => {
        if (!confirm('确认删除该书库及其索引记录？（不会删除网盘或磁盘上的原始文件）')) return;
        try {
          await api(`/admin/api/libraries/${encodeURIComponent(btn.getAttribute('data-lib-del'))}`, { method: 'DELETE' });
          toast('书库已删除');
          renderAdminLibraries(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });
  }

  async function browse115Drawer(cid, stack) {
    const drawer = document.getElementById('nl-browser-drawer');
    if (!drawer) return;
    drawer.style.display = '';
    drawer.innerHTML = `<div class="folder-browser">正在读取 115 目录…</div>`;
    try {
      const res = await api(`/admin/api/115/folders?cid=${encodeURIComponent(cid)}`);
      const folders = res.folders || [];
      drawer.innerHTML = `
        <div class="folder-browser">
          <div style="display:flex;justify-content:space-between;align-items:center;padding:4px 8px;border-bottom:1px solid var(--border)">
            <span style="font-size:12px;color:var(--text-muted)">当前 CID: ${esc(cid)}</span>
            ${stack.length > 1 ? `<button type="button" class="btn btn-sm" id="b115-up">返回上级</button>` : ''}
          </div>
          ${folders.map((f) => `
            <div class="folder-item">
              <span data-b115-open="${esc(f.fileId)}" style="flex:1">📁 ${esc(f.name)} <small style="color:var(--text-muted)">(${esc(f.fileId)})</small></span>
              <button type="button" class="btn btn-sm btn-primary" data-b115-pick="${esc(f.fileId)}" data-b115-name="${esc(f.name)}">选用</button>
            </div>`).join('') || `<div style="padding:12px;color:var(--text-muted)">此目录下没有子文件夹</div>`}
        </div>`;
      const up = document.getElementById('b115-up');
      if (up) up.onclick = () => { stack.pop(); browse115Drawer(stack[stack.length - 1], stack); };
      drawer.querySelectorAll('[data-b115-open]').forEach((el) => {
        el.onclick = () => { const next = el.getAttribute('data-b115-open'); stack.push(next); browse115Drawer(next, stack); };
      });
      drawer.querySelectorAll('[data-b115-pick]').forEach((btn) => {
        btn.onclick = () => {
          document.getElementById('nl-cid').value = btn.getAttribute('data-b115-pick');
          const nameInput = document.getElementById('nl-name');
          if (!nameInput.value.trim()) nameInput.value = btn.getAttribute('data-b115-name');
          drawer.style.display = 'none';
        };
      });
    } catch (err) { drawer.innerHTML = `<div class="folder-browser" style="color:var(--danger);padding:10px">${esc(err.message)}</div>`; }
  }

  async function browseLocalDrawer(dirPath) {
    const drawer = document.getElementById('nl-browser-drawer');
    if (!drawer) return;
    drawer.style.display = '';
    drawer.innerHTML = `<div class="folder-browser">正在读取本地目录…</div>`;
    try {
      const res = await api(`/admin/api/local/folders?path=${encodeURIComponent(dirPath)}`);
      const folders = res.folders || [];
      drawer.innerHTML = `
        <div class="folder-browser">
          <div style="display:flex;justify-content:space-between;align-items:center;padding:4px 8px;border-bottom:1px solid var(--border);gap:8px">
            <code style="font-size:12px">${esc(res.current)}</code>
            <div style="display:flex;gap:6px">
              ${res.parent && res.parent !== res.current ? `<button type="button" class="btn btn-sm" id="bloc-up">上级目录</button>` : ''}
              <button type="button" class="btn btn-sm btn-primary" id="bloc-pick-cur">选用当前目录</button>
            </div>
          </div>
          ${folders.map((f) => `
            <div class="folder-item">
              <span data-bloc-open="${esc(f.path)}" style="flex:1">📁 ${esc(f.name)}</span>
              <button type="button" class="btn btn-sm" data-bloc-pick="${esc(f.path)}" data-bloc-name="${esc(f.name)}">选用</button>
            </div>`).join('') || `<div style="padding:12px;color:var(--text-muted)">无子目录</div>`}
        </div>`;
      const up = document.getElementById('bloc-up');
      if (up) up.onclick = () => browseLocalDrawer(res.parent);
      document.getElementById('bloc-pick-cur').onclick = () => {
        document.getElementById('nl-local-path').value = res.current;
        drawer.style.display = 'none';
      };
      drawer.querySelectorAll('[data-bloc-open]').forEach((el) => {
        el.onclick = () => browseLocalDrawer(el.getAttribute('data-bloc-open'));
      });
      drawer.querySelectorAll('[data-bloc-pick]').forEach((btn) => {
        btn.onclick = () => {
          document.getElementById('nl-local-path').value = btn.getAttribute('data-bloc-pick');
          const nameInput = document.getElementById('nl-name');
          if (!nameInput.value.trim()) nameInput.value = btn.getAttribute('data-bloc-name');
          drawer.style.display = 'none';
        };
      });
    } catch (err) { drawer.innerHTML = `<div class="folder-browser" style="color:var(--danger);padding:10px">${esc(err.message)}</div>`; }
  }

  async function browseWebDAVDrawer(accountID, remotePath) {
    const drawer = document.getElementById('nl-browser-drawer');
    if (!drawer) return;
    drawer.style.display = '';
    drawer.innerHTML = `<div class="folder-browser">正在通过 PROPFIND 读取远程目录…</div>`;
    try {
      const res = await api(`/admin/api/webdav-accounts/${encodeURIComponent(accountID)}/folders?path=${encodeURIComponent(remotePath)}`);
      const folders = res.folders || [];
      drawer.innerHTML = `
        <div class="folder-browser">
          <div style="display:flex;justify-content:space-between;align-items:center;padding:4px 8px;border-bottom:1px solid var(--border);gap:8px">
            <code style="font-size:12px">${esc(res.current)}</code>
            <div style="display:flex;gap:6px">
              ${res.parent && res.parent !== res.current ? `<button type="button" class="btn btn-sm" id="bwd-up">上级目录</button>` : ''}
              <button type="button" class="btn btn-sm btn-primary" id="bwd-pick-cur">选用当前路径</button>
            </div>
          </div>
          ${folders.map((f) => `
            <div class="folder-item">
              <span data-bwd-open="${esc(f.path)}" style="flex:1">📁 ${esc(f.name)}</span>
              <button type="button" class="btn btn-sm" data-bwd-pick="${esc(f.path)}" data-bwd-name="${esc(f.name)}">选用</button>
            </div>`).join('') || `<div style="padding:12px;color:var(--text-muted)">无子目录</div>`}
        </div>`;
      const up = document.getElementById('bwd-up');
      if (up) up.onclick = () => browseWebDAVDrawer(accountID, res.parent);
      document.getElementById('bwd-pick-cur').onclick = () => {
        document.getElementById('nl-wd-path').value = res.current;
        drawer.style.display = 'none';
      };
      drawer.querySelectorAll('[data-bwd-open]').forEach((el) => {
        el.onclick = () => browseWebDAVDrawer(accountID, el.getAttribute('data-bwd-open'));
      });
      drawer.querySelectorAll('[data-bwd-pick]').forEach((btn) => {
        btn.onclick = () => {
          document.getElementById('nl-wd-path').value = btn.getAttribute('data-bwd-pick');
          const nameInput = document.getElementById('nl-name');
          if (!nameInput.value.trim()) nameInput.value = btn.getAttribute('data-bwd-name');
          drawer.style.display = 'none';
        };
      });
    } catch (err) { drawer.innerHTML = `<div class="folder-browser" style="color:var(--danger);padding:10px">${esc(err.message)}</div>`; }
  }

  // --- Admin Bangumi Scraper Center ---

  async function renderAdminBangumi(body) {
    const [st, runs, libs] = await Promise.all([
      api('/admin/api/bangumi/status'),
      api('/admin/api/bangumi/runs?limit=20'),
      api('/admin/api/libraries'),
    ]);
    paintAdminBangumi(body, st, runs || [], libs || []);
  }

  function paintAdminBangumi(body, st, runs, libs) {
    const arch = st.archive || {};
    const dlPct = arch.totalBytes > 0 ? Math.min(100, Math.round((arch.downloadedBytes / arch.totalBytes) * 100)) : 0;
    const prevInput = document.getElementById('bgm-proxy-url');
    const editingProxy = prevInput && document.activeElement === prevInput;
    const proxyInputVal = editingProxy ? prevInput.value : (st.proxyUrl || '');
    const selStart = editingProxy ? prevInput.selectionStart : null;
    const selEnd = editingProxy ? prevInput.selectionEnd : null;

    body.innerHTML = `
      <div class="stat-grid">
        <div class="stat-box"><div class="k">总系列数</div><div class="v">${Number(st.totalSeries || 0).toLocaleString()}${(!st.showNsfw && st.nsfwCount > 0) ? `<span style="font-size:12px;font-weight:500;color:var(--text-muted);margin-left:6px">（已隐藏 NSFW ${Number(st.nsfwCount).toLocaleString()} 部）</span>` : ''}</div></div>
        <div class="stat-box"><div class="k">已匹配 Bangumi</div><div class="v" style="color:var(--success)">${Number(st.matchedCount || 0).toLocaleString()}</div></div>
        <div class="stat-box"><div class="k">待刮削</div><div class="v">${Number(st.pendingCount || 0).toLocaleString()}</div></div>
        <div class="stat-box"><div class="k">自动未命中</div><div class="v" style="color:var(--warn)">${Number(st.unmatchedCount || 0).toLocaleString()}</div></div>
      </div>

      <div class="card">
        <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:12px">
          <div>
            <h3 style="margin:0 0 4px">NSFW 作品前台显示开关</h3>
            <div style="font-size:12.5px;color:var(--text-secondary)">
              系统正常匹配 Bangumi <code>nsfw: true</code> 条目及含成人目录特征的作品（非 <code>nsfw: true</code> 的普通受限标签作品不受影响）。默认关闭以在前台书架与 Komga 客户端中自动隐藏 NSFW 作品（当前未隐藏书库中共识别 <strong>${Number(st.nsfwCount || 0).toLocaleString()}</strong> 部 NSFW 作品）。
            </div>
          </div>
          <label class="check-row" style="font-weight:600;color:var(--text)">
            <input type="checkbox" id="bgm-show-nsfw" ${st.showNsfw ? 'checked' : ''}>
            <span>显示 NSFW 作品</span>
          </label>
        </div>
      </div>

      <div class="card">
        <div class="section-head" style="margin-bottom:8px">
          <h3>Bangumi 专用网络代理设置</h3>
          <span class="badge ${st.proxyUrl ? 'badge-success' : ''}">${st.proxyUrl ? '已启用代理' : '当前为直连'}</span>
        </div>
        <p style="font-size:12.5px;color:var(--text-secondary);margin:0 0 12px">
          统一加速 <code>bangumi/Archive</code> GitHub 离线快照下载、<code>api.bgm.tv</code> 检索与 <code>lain.bgm.tv</code> 封面抓取（含手动匹配弹窗预览图）。<strong>仅作用于 Bangumi 相关请求，绝不影响 115 网盘与 WebDAV 流量。</strong>
        </p>
        <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center">
          <input type="text" id="bgm-proxy-url" value="${esc(proxyInputVal)}" placeholder="留空直连；支持 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080" style="flex:1;min-width:260px;height:36px;padding:0 11px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text)">
          <button type="button" class="btn btn-primary" id="bgm-proxy-save">保存代理</button>
          <button type="button" class="btn" id="bgm-proxy-test">测试连通性与延迟</button>
          ${st.proxyUrl ? `<button type="button" class="btn btn-danger" id="bgm-proxy-clear">清空改回直连</button>` : ''}
        </div>
        <div id="bgm-proxy-probe-result" style="margin-top:10px;font-size:12.5px">${state.bgmProxyProbeHTML || ''}</div>
      </div>

      <div class="grid-2">
        <div class="card">
          <h3>引擎一：Bangumi 官方离线索引库（推荐大规模书库使用）</h3>
          <p style="font-size:12.5px;color:var(--text-secondary)">
            自动从 <code>bangumi/Archive</code> 拉取最新公开数据快照并校验 SHA256，流式导入全量书籍主条目（含 <code>nsfw: true</code> 分级标记）并剔除单行本分卷子条目。几万部漫画可在本地 SQLite 毫秒级完成繁简/异体归一与作者交叉匹配，零 API 限流风险。
          </p>
          <div style="font-size:13px;margin:12px 0;display:grid;gap:4px">
            <div>状态：${arch.ready ? `<span class="badge badge-success">已就绪 · ${Number(arch.subjectCount || 0).toLocaleString()} 条书籍条目</span>` : `<span class="badge badge-warn">尚未建立离线索引</span>`}</div>
            ${arch.dumpName ? `<div style="color:var(--text-muted);font-size:12px">快照版本：${esc(arch.dumpName)} · 导入于 ${fmtTime(arch.importedAt)}</div>` : ''}
            ${arch.updating ? `
              <div style="margin-top:6px">
                <div style="font-size:12px;color:var(--accent);margin-bottom:4px">正在更新：${esc(arch.phase || '下载/解析中')} ${arch.totalBytes > 0 ? `(${fmtBytes(arch.downloadedBytes)} / ${fmtBytes(arch.totalBytes)} · ${dlPct}%)` : ''}</div>
                <div class="progress-track"><span style="width:${dlPct || 35}%"></span></div>
              </div>` : ''}
            ${arch.lastError ? `<div style="color:var(--danger);font-size:12px">最近错误：${esc(arch.lastError)}</div>` : ''}
          </div>
          <div style="display:flex;gap:8px">
            <button type="button" class="btn btn-primary" id="bgm-arch-update" ${arch.updating ? 'disabled' : ''}>${arch.ready ? '检查并更新离线索引库' : '一键下载并构建离线索引库'}</button>
            ${arch.updating ? `<button type="button" class="btn btn-danger" id="bgm-arch-cancel">取消下载</button>` : ''}
          </div>
        </div>

        <div class="card">
          <h3>启动批量自动刮削</h3>
          <p style="font-size:12.5px;color:var(--text-secondary)">
            支持 <code>[标题][作者]</code> 与 <code>[作者][标题]</code> 双序识别、通用子目录（如 <code>台版</code>/<code>特别篇</code>）自动回溯父目录并追加后缀、繁简异体字归一与作者交叉打分，不会覆盖已手动绑定或锁定的字段。
          </p>
          <div class="field">
            <label>选择目标书库</label>
            <select id="bgm-batch-lib">
              <option value="">全部未隐藏书库</option>
              ${(libs || []).filter((l) => l.enabled !== false).map((l) => `<option value="${esc(l.id)}">${esc(l.name)}</option>`).join('')}
            </select>
          </div>
          <div class="field">
            <label>刮削范围模式</label>
            <select id="bgm-batch-mode">
              <option value="incremental">增量刮削（仅处理尚未刮削的新作品）</option>
              <option value="retry_unmatched">重试未命中（处理“自动未命中”与新作品，规则升级后推荐）</option>
              <option value="force">全量强制重刮（重新匹配全部作品，保留手动绑定与锁定字段）</option>
            </select>
          </div>
          <button type="button" class="btn btn-bgm" id="bgm-batch-start">开始批量刮削</button>
        </div>
      </div>

      <div class="card">
        <div class="section-head">
          <h3>刮削任务记录</h3>
          <button type="button" class="btn btn-sm" id="bgm-refresh-btn">刷新</button>
        </div>
        ${runs.length === 0
          ? `<div class="empty-box">暂无批量刮削任务记录。</div>`
          : `<div class="table-wrap"><table class="data-table">
              <thead><tr><th>目标</th><th>模式</th><th>状态</th><th>进度</th><th>命中 / 未命中 / 跳过</th><th>当前处理 / 开始时间</th><th>操作</th></tr></thead>
              <tbody>
                ${runs.map((r) => {
                  const pct = r.totalSeries > 0 ? Math.min(100, Math.round((r.processedSeries / r.totalSeries) * 100)) : 0;
                  return `
                    <tr>
                      <td><strong>${esc(r.libraryName || '全部未隐藏书库')}</strong></td>
                      <td><span class="badge">${esc(r.mode)}</span></td>
                      <td><span class="badge ${(r.status === 'completed' || r.status === 'success') ? 'badge-success' : r.status === 'failed' ? 'badge-danger' : 'badge-accent'}">${esc(r.status)}</span></td>
                      <td style="min-width:140px">
                        <div style="font-size:12px">${r.processedSeries} / ${r.totalSeries} (${pct}%)</div>
                        <div class="progress-track" style="margin-top:3px"><span style="width:${pct}%"></span></div>
                      </td>
                      <td><span style="color:var(--success)">${r.matchedCount}</span> / <span style="color:var(--warn)">${r.unmatchedCount}</span> / ${r.skippedCount}</td>
                      <td style="font-size:12px;color:var(--text-muted)">${esc(r.currentSeries || fmtTime(r.startedAt || r.createdAt))}</td>
                      <td>${(r.status === 'queued' || r.status === 'running') ? `<button type="button" class="btn btn-sm btn-danger" data-cancel-bgm-run="${esc(r.id)}">取消</button>` : ''}</td>
                    </tr>`;
                }).join('')}
              </tbody>
            </table></div>`}
      </div>
    `;

    if (editingProxy) {
      const restored = document.getElementById('bgm-proxy-url');
      if (restored) {
        restored.focus();
        if (selStart !== null && selEnd !== null) {
          try { restored.setSelectionRange(selStart, selEnd); } catch (_) {}
        }
      }
    }

    document.getElementById('bgm-show-nsfw').onchange = async (e) => {
      const showNsfw = e.target.checked;
      try {
        await api('/admin/api/bangumi/config', { method: 'PUT', body: { showNsfw } });
        state.taxonomies.loaded = false;
        toast(showNsfw ? '已开启显示 NSFW 作品' : '已隐藏 NSFW 作品');
        renderAdminBangumi(body);
      } catch (err) {
        e.target.checked = !showNsfw;
        toast(err.message, 'error');
      }
    };

    document.getElementById('bgm-proxy-save').onclick = async () => {
      const proxyUrl = document.getElementById('bgm-proxy-url').value.trim();
      try {
        await api('/admin/api/bangumi/config', { method: 'PUT', body: { proxyUrl } });
        toast(proxyUrl ? 'Bangumi 代理已保存并立即生效' : '已切换为直连模式');
        renderAdminBangumi(body);
      } catch (err) { toast(err.message, 'error'); }
    };
    const clearProxyBtn = document.getElementById('bgm-proxy-clear');
    if (clearProxyBtn) {
      clearProxyBtn.onclick = async () => {
        try {
          state.bgmProxyProbeHTML = '';
          await api('/admin/api/bangumi/config', { method: 'PUT', body: { proxyUrl: '' } });
          toast('已清空代理并恢复直连');
          renderAdminBangumi(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    }
    document.getElementById('bgm-proxy-test').onclick = async () => {
      const btn = document.getElementById('bgm-proxy-test');
      const probeBox = document.getElementById('bgm-proxy-probe-result');
      const proxyUrl = document.getElementById('bgm-proxy-url').value.trim();
      btn.disabled = true;
      if (probeBox) probeBox.innerHTML = `<span style="color:var(--text-secondary)">正在测试 GitHub 与 api.bgm.tv 连通性…</span>`;
      try {
        const rep = await api('/admin/api/bangumi/proxy/test', { method: 'POST', body: { proxyUrl } });
        const fmtItem = (label, p) => p && p.ok
          ? `<span style="color:var(--success)">✅ ${label}：连通 (${p.latencyMs} ms${p.detail ? ` · ${esc(p.detail)}` : ''})</span>`
          : `<span style="color:var(--danger)">❌ ${label}：失败 (${esc((p && p.error) || '超时')})</span>`;
        state.bgmProxyProbeHTML = `<div style="display:flex;flex-wrap:wrap;gap:16px;padding:8px 11px;border-radius:8px;background:var(--bg-input);border:1px solid var(--border)">
          <span>模式：<strong>${rep.direct ? '直连' : esc(rep.proxyUrl)}</strong></span>
          ${fmtItem('api.bgm.tv', rep.api)}
          ${fmtItem('GitHub 离线库源', rep.archive)}
        </div>`;
        if (probeBox) probeBox.innerHTML = state.bgmProxyProbeHTML;
      } catch (err) {
        toast(err.message, 'error');
        if (probeBox) probeBox.innerHTML = `<span style="color:var(--danger)">${esc(err.message)}</span>`;
      } finally {
        btn.disabled = false;
      }
    };

    document.getElementById('bgm-arch-update').onclick = async () => {
      try {
        await api('/admin/api/bangumi/archive/update', { method: 'POST' });
        toast('离线索引库后台下载构建已启动');
        renderAdminBangumi(body);
      } catch (err) { toast(err.message, 'error'); }
    };
    const cancelArch = document.getElementById('bgm-arch-cancel');
    if (cancelArch) {
      cancelArch.onclick = async () => {
        try { await api('/admin/api/bangumi/archive/cancel', { method: 'POST' }); renderAdminBangumi(body); }
        catch (err) { toast(err.message, 'error'); }
      };
    }
    document.getElementById('bgm-batch-start').onclick = async () => {
      try {
        const mode = document.getElementById('bgm-batch-mode').value;
        await api('/admin/api/bangumi/runs', {
          method: 'POST',
          body: {
            libraryId: document.getElementById('bgm-batch-lib').value,
            mode,
            force: mode === 'force',
          },
        });
        toast('批量刮削任务已启动');
        renderAdminBangumi(body);
      } catch (err) { toast(err.message, 'error'); }
    };
    document.getElementById('bgm-refresh-btn').onclick = () => renderAdminBangumi(body);
    body.querySelectorAll('button[data-cancel-bgm-run]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api(`/admin/api/bangumi/runs/${encodeURIComponent(btn.getAttribute('data-cancel-bgm-run'))}/cancel`, { method: 'POST' });
          renderAdminBangumi(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    const hasActive = Boolean(arch.updating) || runs.some((r) => r.status === 'queued' || r.status === 'running');
    if (hasActive) {
      schedulePoll(async () => {
        if (location.pathname !== '/admin' || state.adminTab !== 'bangumi') return;
        await renderAdminBangumi(body);
      }, 2200);
    } else {
      stopPolling();
    }
  }

  // --- Admin Jobs Tab (Adaptive Polling ONLY when active!) ---

  async function renderAdminJobs(body) {
    const [scans, covers, maints] = await Promise.all([
      api('/admin/api/scans?limit=25'),
      api('/admin/api/cover-jobs?limit=20'),
      api('/admin/api/maintenance-jobs?limit=20'),
    ]);
    paintAdminJobs(body, scans || [], covers || [], maints || []);
  }

  function paintAdminJobs(body, scans, covers, maints) {
    const isActive = (r) => r.status === 'queued' || r.status === 'running';
    const anyActive = scans.some(isActive) || covers.some(isActive) || maints.some(isActive);

    body.innerHTML = `
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:14px">
        <span style="font-size:13px;color:var(--text-secondary)">
          ${anyActive ? '⚡ 检测到运行中任务，正在自动同步进度…' : '当前无运行中任务（空闲时不会后台轮询刷屏）'}
        </span>
        <button type="button" class="btn btn-sm" id="jobs-refresh-btn">手动刷新</button>
      </div>

      <div class="card">
        <h3>书库扫描任务</h3>
        ${scans.length === 0 ? `<div class="empty-box">暂无扫描记录。</div>` : `
          <div class="table-wrap"><table class="data-table">
            <thead><tr><th>书库</th><th>状态</th><th>统计</th><th>开始时间</th><th>错误信息</th><th>操作</th></tr></thead>
            <tbody>
              ${scans.map((s) => `
                <tr>
                  <td><strong>${esc(s.libraryName || s.libraryId)}</strong></td>
                  <td><span class="badge ${(s.status === 'completed' || s.status === 'success') ? 'badge-success' : s.status === 'failed' ? 'badge-danger' : 'badge-accent'}">${esc(s.status)}</span></td>
                  <td>目录 ${s.directoriesSeen ?? s.directoriesScanned ?? 0} · 系列 ${s.seriesSeen ?? s.seriesFound ?? 0} · 单册 ${s.booksSeen ?? s.booksFound ?? 0}</td>
                  <td>${fmtTime(s.startedAt)}</td>
                  <td style="color:var(--danger);font-size:12px">${esc(s.error || '')}</td>
                  <td>${isActive(s) ? `<button type="button" class="btn btn-sm btn-danger" data-cancel-scan="${esc(s.id)}">取消</button>` : ''}</td>
                </tr>`).join('')}
            </tbody>
          </table></div>`}
      </div>

      <div class="card">
        <h3>批量封面生成任务</h3>
        ${covers.length === 0 ? `<div class="empty-box">暂无批量封面任务记录。</div>` : `
          <div class="table-wrap"><table class="data-table">
            <thead><tr><th>书库</th><th>状态</th><th>进度</th><th>生成 / 跳过 / 失败</th><th>当前处理</th><th>操作</th></tr></thead>
            <tbody>
              ${covers.map((c) => `
                <tr>
                  <td><strong>${esc(c.libraryName || c.libraryId)}</strong></td>
                  <td><span class="badge ${(c.status === 'completed' || c.status === 'success') ? 'badge-success' : c.status === 'failed' ? 'badge-danger' : 'badge-accent'}">${esc(c.status)}</span></td>
                  <td>${c.processedSeries || 0} / ${c.totalSeries || 0}</td>
                  <td>${c.generatedCount || 0} / ${c.skippedCount || 0} / ${c.failedCount || 0}</td>
                  <td style="font-size:12px;color:var(--text-muted)">${esc(c.currentSeries || fmtTime(c.startedAt))}</td>
                  <td>${isActive(c) ? `<button type="button" class="btn btn-sm btn-danger" data-cancel-cover="${esc(c.id)}">取消</button>` : ''}</td>
                </tr>`).join('')}
            </tbody>
          </table></div>`}
      </div>

      <div class="card">
        <h3>归档索引与单部维护任务</h3>
        ${maints.length === 0 ? `<div class="empty-box">暂无维护任务记录。</div>` : renderMaintJobRows(maints)}
      </div>
    `;

    document.getElementById('jobs-refresh-btn').onclick = () => renderAdminJobs(body);
    body.querySelectorAll('button[data-cancel-scan]').forEach((btn) => {
      btn.onclick = async () => {
        try { await api(`/admin/api/scans/${encodeURIComponent(btn.getAttribute('data-cancel-scan'))}/cancel`, { method: 'POST' }); renderAdminJobs(body); }
        catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-cancel-cover]').forEach((btn) => {
      btn.onclick = async () => {
        try { await api(`/admin/api/cover-jobs/${encodeURIComponent(btn.getAttribute('data-cancel-cover'))}/cancel`, { method: 'POST' }); renderAdminJobs(body); }
        catch (err) { toast(err.message, 'error'); }
      };
    });
    bindMaintCancelButtons(body, () => renderAdminJobs(body));

    if (anyActive) {
      schedulePoll(async () => {
        if (location.pathname !== '/admin' || state.adminTab !== 'jobs') return;
        await renderAdminJobs(body);
      }, 2400);
    } else {
      stopPolling();
    }
  }

  // --- Admin Cache & Settings Tab ---

  async function renderAdminSettings(body) {
    const [cache, settings] = await Promise.all([
      api('/admin/api/cache'),
      api('/admin/api/settings'),
    ]);
    const entries = cache.entries || {};
    const rangeStat = entries.range || {};
    const pageStat = entries.page || {};
    const thumbStat = cache.seriesThumbnails || {};
    const bgmThumb = cache.bangumiCovers || {};

    body.innerHTML = `
      <div class="card">
        <div class="section-head">
          <h3>磁盘与数据库缓存统计</h3>
          <button type="button" class="btn btn-sm btn-danger" data-clear-cache="all">清空全部缓存</button>
        </div>
        <div class="stat-grid">
          <div class="stat-box">
            <div class="k">Range 分块缓存（115 / WebDAV）</div>
            <div class="v" style="font-size:17px">${fmtMiB(rangeStat.bytes)} · ${rangeStat.files ?? rangeStat.count ?? 0} 块</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="range">清理 Range</button>
          </div>
          <div class="stat-box">
            <div class="k">解压页面缓存</div>
            <div class="v" style="font-size:17px">${fmtMiB(pageStat.bytes)} · ${pageStat.files ?? pageStat.count ?? 0} 页</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="page">清理页面缓存</button>
          </div>
          <div class="stat-box">
            <div class="k">归档目录索引 (ZIP/RAR)</div>
            <div class="v" style="font-size:17px">${Number(cache.archiveIndexes || 0).toLocaleString()} 本</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="archive">清理索引</button>
          </div>
          <div class="stat-box">
            <div class="k">首卷缩略图缓存</div>
            <div class="v" style="font-size:17px">${fmtMiB(thumbStat.bytes)} · ${thumbStat.files ?? thumbStat.count ?? 0} 张</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="thumbnail">清理首卷封面</button>
          </div>
          <div class="stat-box">
            <div class="k">Bangumi 懒加载封面缓存</div>
            <div class="v" style="font-size:17px">${fmtMiB(bgmThumb.bytes)} · ${bgmThumb.files ?? bgmThumb.count ?? 0} 张</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="bangumi">清理 Bgm 封面</button>
          </div>
        </div>
      </div>

      <form class="card" id="sys-settings-form">
        <h3>运行参数与缓存策略</h3>
        <div class="grid-2">
          <div class="field"><label>115 API 限速 (req/s)</label><input type="text" name="api_rate_per_second" value="${esc(settings.api_rate_per_second || '3')}"></div>
          <div class="field"><label>下载直链缓存秒数 (downurl_ttl_seconds)</label><input type="number" name="downurl_ttl_seconds" value="${esc(settings.downurl_ttl_seconds || '1800')}"></div>
          <div class="field"><label>Range 分块大小 (MiB)</label><input type="number" step="0.25" min="0.25" name="range_block_size_mib" value="${esc(bytesToMiBInput(settings.range_block_size, 2))}"></div>
          <div class="field"><label>Range 缓存上限 (MiB)</label><input type="number" step="1" min="0" name="range_cache_max_mib" value="${esc(bytesToMiBInput(settings.range_cache_max_bytes, 2048))}"></div>
          <div class="field"><label>页面缓存上限 (MiB)</label><input type="number" step="1" min="0" name="page_cache_max_mib" value="${esc(bytesToMiBInput(settings.page_cache_max_bytes, 1024))}"></div>
          <div class="field"><label>向后预读页数 (page_prefetch_count)</label><input type="number" name="page_prefetch_count" value="${esc(settings.page_prefetch_count || '2')}"></div>
          <div class="field"><label>单页最大允许体积 (MiB)</label><input type="number" step="1" min="1" name="max_page_size_mib" value="${esc(bytesToMiBInput(settings.max_page_size, 50))}"></div>
          <div class="field"><label>下一卷索引预热阈值（剩余页数 1-100）</label><input type="number" name="volume_index_prefetch_remaining_pages" value="${esc(settings.volume_index_prefetch_remaining_pages || '10')}"></div>
          <div class="field"><label>定时自动扫描间隔（分钟）</label><input type="number" name="auto_scan_interval_minutes" value="${esc(settings.auto_scan_interval_minutes || '360')}"></div>
        </div>
        <div style="display:flex;flex-wrap:wrap;gap:18px;margin:10px 0 16px">
          <label class="check-row"><input type="checkbox" name="show_nsfw" ${settings.show_nsfw === 'true' ? 'checked' : ''}><span>显示 NSFW 作品（默认隐藏）</span></label>
          <label class="check-row"><input type="checkbox" name="volume_index_prefetch_enabled" ${settings.volume_index_prefetch_enabled === 'true' ? 'checked' : ''}><span>启用下一卷目录索引自动预热</span></label>
          <label class="check-row"><input type="checkbox" name="auto_scan_enabled" ${settings.auto_scan_enabled === 'true' ? 'checked' : ''}><span>启用定时自动扫描</span></label>
          <label class="check-row"><input type="checkbox" name="scan_on_startup" ${settings.scan_on_startup === 'true' ? 'checked' : ''}><span>进程启动时自动扫描</span></label>
        </div>
        <button type="submit" class="btn btn-primary">保存系统设置</button>
      </form>
    `;

    body.querySelectorAll('button[data-clear-cache]').forEach((btn) => {
      btn.onclick = async () => {
        const ct = btn.getAttribute('data-clear-cache');
        if (!confirm(`确认清理缓存 (${ct})？`)) return;
        try {
          await api(`/admin/api/cache/${encodeURIComponent(ct)}`, { method: 'DELETE' });
          state.coverVer = Date.now();
          toast('缓存清理完成');
          renderAdminSettings(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    document.getElementById('sys-settings-form').onsubmit = async (e) => {
      e.preventDefault();
      const fd = new FormData(e.target);
      const payload = {};
      ['api_rate_per_second', 'downurl_ttl_seconds', 'page_prefetch_count', 'volume_index_prefetch_remaining_pages', 'auto_scan_interval_minutes']
        .forEach((k) => { payload[k] = String(fd.get(k) || '').trim(); });
      const mibPairs = [
        ['range_block_size', 'range_block_size_mib', 'Range 分块大小'],
        ['range_cache_max_bytes', 'range_cache_max_mib', 'Range 缓存上限'],
        ['page_cache_max_bytes', 'page_cache_max_mib', '页面缓存上限'],
        ['max_page_size', 'max_page_size_mib', '单页最大允许体积'],
      ];
      for (const [byteKey, mibField, label] of mibPairs) {
        const converted = mibInputToBytes(fd.get(mibField));
        if (converted === null) {
          toast(`${label} 请输入有效的非负数字 (MiB)`, 'error');
          return;
        }
        payload[byteKey] = converted;
      }
      ['show_nsfw', 'volume_index_prefetch_enabled', 'auto_scan_enabled', 'scan_on_startup']
        .forEach((k) => { payload[k] = e.target.elements[k].checked ? 'true' : 'false'; });
      try {
        await api('/admin/api/settings', { method: 'PUT', body: payload });
        state.taxonomies.loaded = false;
        toast('系统设置已保存');
        renderAdminSettings(body);
      } catch (err) { toast(err.message, 'error'); }
    };
  }

  // --- Admin Security & API Keys Tab ---

  async function renderAdminSecurity(body) {
    const [keys, sessions] = await Promise.all([
      api('/admin/api/auth/api-keys'),
      api('/admin/api/auth/sessions'),
    ]);

    body.innerHTML = `
      <div class="grid-2">
        <form class="card" id="sec-cred-form">
          <h3>修改管理员用户名 / 密码</h3>
          <div class="field">
            <label>当前密码（必填）</label>
            <input type="password" id="sec-cur-pass" autocomplete="current-password" required>
          </div>
          <div class="field">
            <label>新用户名（留空保持 <code>${esc(state.auth.username)}</code>）</label>
            <input type="text" id="sec-new-user" placeholder="${esc(state.auth.username)}">
          </div>
          <div class="field">
            <label>新密码（留空不修改密码，修改需至少 8 位）</label>
            <input type="password" id="sec-new-pass" autocomplete="new-password" placeholder="至少 8 位字符">
          </div>
          <button type="submit" class="btn btn-primary">更新凭据（自动注销其他会话）</button>
        </form>

        <div class="card">
          <h3>API Key 管理（推荐用于 AI Agent / 外部客户端 / 脚本）</h3>
          <p style="font-size:12.5px;color:var(--text-secondary)">支持通过 <code>Authorization: Bearer &lt;密钥&gt;</code> 或 <code>X-API-Key: &lt;密钥&gt;</code> 访问 Komga 接口与 AI Agent 专用书库管理接口（<code>GET /api/agent/v1/manifest</code>），无需暴露主登录密码，可随时单独吊销。</p>
          <div style="display:flex;gap:8px;margin-bottom:12px">
            <input type="text" id="ak-name" placeholder="备注名称，例如：Mihon 平板" style="flex:1;height:36px;padding:0 10px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text)">
            <button type="button" class="btn btn-primary" id="ak-create">生成 API Key</button>
          </div>
          <div id="ak-revealed" style="display:none;margin-bottom:12px"></div>
          ${(keys || []).length === 0 ? `<div class="empty-box" style="padding:18px">暂未创建 API Key。</div>` : `
            <div class="table-wrap"><table class="data-table">
              <thead><tr><th>名称</th><th>前缀</th><th>最近使用</th><th>操作</th></tr></thead>
              <tbody>
                ${keys.map((k) => `
                  <tr>
                    <td><strong>${esc(k.name)}</strong></td>
                    <td><code>${esc(k.keyPrefix)}…</code></td>
                    <td style="font-size:12px;color:var(--text-muted)">${fmtTime(k.lastUsedAt || k.createdAt)}</td>
                    <td><button type="button" class="btn btn-sm btn-danger" data-del-ak="${esc(k.id)}">吊销</button></td>
                  </tr>`).join('')}
              </tbody>
            </table></div>`}
        </div>
      </div>

      <div class="card">
        <h3>活跃登录会话（${(sessions || []).length}）</h3>
        <div class="table-wrap"><table class="data-table">
          <thead><tr><th>会话标识</th><th>浏览器 / 客户端</th><th>最近活跃</th><th>过期时间</th><th>操作</th></tr></thead>
          <tbody>
            ${(sessions || []).map((s) => {
              const sid = s.id || s.tokenPrefix || '';
              const expTxt = (!s.expiresAt || String(s.expiresAt).startsWith('9999-')) ? '永久有效（自动续期）' : fmtTime(s.expiresAt);
              return `
              <tr>
                <td><code>${esc(sid)}</code> ${s.current ? '<span class="badge badge-success">当前设备</span>' : ''}</td>
                <td style="font-size:12px;color:var(--text-secondary);max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(s.userAgent || '—')}</td>
                <td>${fmtTime(s.lastSeenAt)}</td>
                <td>${esc(expTxt)}</td>
                <td>${!s.current && sid ? `<button type="button" class="btn btn-sm btn-danger" data-del-sess="${esc(sid)}">强制下线</button>` : ''}</td>
              </tr>`;
            }).join('')}
          </tbody>
        </table></div>
      </div>
    `;

    document.getElementById('sec-cred-form').onsubmit = async (e) => {
      e.preventDefault();
      const currentPassword = document.getElementById('sec-cur-pass').value;
      const newUsername = document.getElementById('sec-new-user').value.trim();
      const newPassword = document.getElementById('sec-new-pass').value;
      try {
        const updated = await api('/admin/api/auth/credentials', {
          method: 'PUT',
          body: { currentPassword, newUsername, newPassword },
        });
        if (updated && updated.username) state.auth.username = updated.username;
        toast('管理员凭据已更新');
        renderAdminSecurity(body);
      } catch (err) { toast(err.message, 'error'); }
    };

    document.getElementById('ak-create').onclick = async () => {
      const name = document.getElementById('ak-name').value.trim();
      if (!name) { toast('请填写 API Key 备注名称', 'error'); return; }
      try {
        const res = await api('/admin/api/auth/api-keys', { method: 'POST', body: { name } });
        await renderAdminSecurity(body);
        const rev = document.getElementById('ak-revealed');
        if (rev) {
          rev.style.display = '';
          rev.innerHTML = `
            <div style="padding:10px 12px;border-radius:8px;background:var(--success-soft);border:1px solid var(--success)">
              <div style="font-size:12px;font-weight:600;color:var(--success);margin-bottom:4px">请立即复制保存，此完整密钥仅显示一次：</div>
              <div class="code-box" style="user-select:all">${esc(res.key)}</div>
            </div>`;
        }
      } catch (err) { toast(err.message, 'error'); }
    };

    body.querySelectorAll('button[data-del-ak]').forEach((btn) => {
      btn.onclick = async () => {
        if (!confirm('确认吊销该 API Key？')) return;
        try {
          await api(`/admin/api/auth/api-keys/${encodeURIComponent(btn.getAttribute('data-del-ak'))}`, { method: 'DELETE' });
          toast('API Key 已吊销');
          renderAdminSecurity(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    body.querySelectorAll('button[data-del-sess]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api(`/admin/api/auth/sessions/${encodeURIComponent(btn.getAttribute('data-del-sess'))}`, { method: 'DELETE' });
          toast('会话已移除');
          renderAdminSecurity(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });
  }

  // --- Router ---

  async function route() {
    try {
      const st = await api('/admin/api/auth/status', { allow401: true });
      state.auth = st || { claimed: false, authenticated: false, username: '' };
    } catch (_) {}

    const p = location.pathname;
    if (!state.auth.claimed) {
      if (p !== '/setup') {
        history.replaceState(null, '', '/setup');
      }
      renderSetup();
      return;
    }

    if (p === '/setup') {
      navigate(state.auth.authenticated ? '/' : '/login', true);
      return;
    }

    if (p === '/login') {
      if (state.auth.authenticated) {
        const redir = new URLSearchParams(location.search).get('redirect') || '/';
        navigate(redir.startsWith('/') && !redir.startsWith('//') ? redir : '/', true);
        return;
      }
      renderLogin();
      return;
    }

    if (!state.auth.authenticated) {
      const redir = encodeURIComponent(p + location.search);
      history.replaceState(null, '', `/login?redirect=${redir}`);
      renderLogin();
      return;
    }

    if (p === '/' || p === '') {
      await renderHome();
      return;
    }
    if (p === '/libraries') {
      await renderLibraries('');
      return;
    }
    if (p.startsWith('/libraries/')) {
      const libID = decodeURIComponent(p.slice('/libraries/'.length));
      await renderLibraries(libID);
      return;
    }
    if (p.startsWith('/series/')) {
      const sid = decodeURIComponent(p.slice('/series/'.length));
      await renderSeriesDetail(sid);
      return;
    }
    if (p.startsWith('/book/')) {
      const bid = decodeURIComponent(p.slice('/book/'.length));
      await renderBookDetail(bid);
      return;
    }
    if (p.startsWith('/books/')) {
      const bid = decodeURIComponent(p.slice('/books/'.length));
      await renderBookDetail(bid);
      return;
    }
    if (p === '/admin' || p === '/admin/') {
      await renderAdmin();
      return;
    }

    renderShell('', `<div class="empty-box">页面不存在（404） · <a href="/" data-nav style="color:var(--accent)">返回书架首页</a></div>`);
  }

  route();
})();
