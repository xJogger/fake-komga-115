// ==UserScript==
// @name         MangaAlpha Bangumi 一键跳转
// @namespace    https://github.com/xJogger/MangaAlpha
// @version      2.3.0
// @description  在 Bangumi 条目页自动探测本地 MangaAlpha 收录状态，并一键直达对应漫画。
// @match        https://bgm.tv/subject/*
// @match        https://bangumi.tv/subject/*
// @match        https://chii.in/subject/*
// @grant        GM_xmlhttpRequest
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_registerMenuCommand
// @connect      *
// @run-at       document-end
// ==/UserScript==

(function () {
  'use strict';

  const DEFAULT_SERVER_URL = '__MANGAALPHA_SERVER_URL__';
  const DEFAULT_API_KEY = '__MANGAALPHA_API_KEY__';

  const m = location.pathname.match(/^\/subject\/(\d+)\/?$/);
  if (!m) return;
  const subjectId = m[1];

  const header = document.querySelector('#headerSubject');
  const titleEl = document.querySelector('#headerSubject h1.nameSingle a') || document.querySelector('#headerSubject h1.nameSingle');
  if (!header || !titleEl) return;
  const pageTitle = (titleEl.textContent || '').trim();

  function cleanServerUrl(raw) {
    return String(raw || '').trim().replace(/\/+$/, '');
  }

  function getServerUrl() {
    const saved = cleanServerUrl(GM_getValue('server_url', ''));
    if (saved) return saved;
    if (DEFAULT_SERVER_URL && !DEFAULT_SERVER_URL.includes('__MANGAALPHA_')) {
      return cleanServerUrl(DEFAULT_SERVER_URL);
    }
    return '';
  }

  function getApiKey() {
    const saved = String(GM_getValue('api_key', '') || '').trim();
    if (saved) return saved;
    if (DEFAULT_API_KEY && !DEFAULT_API_KEY.includes('__MANGAALPHA_')) {
      return DEFAULT_API_KEY.trim();
    }
    return '';
  }

  function promptConfig() {
    const curUrl = getServerUrl();
    const nextUrl = window.prompt('请输入 MangaAlpha 服务器地址（例如 http://127.0.0.1:25600）：', curUrl);
    if (nextUrl === null) return;
    GM_setValue('server_url', cleanServerUrl(nextUrl));
    const curKey = getApiKey();
    const nextKey = window.prompt('请输入 MangaAlpha API Key（留空则仅显示普通跳转按钮，不自动探测收录状态）：', curKey);
    if (nextKey !== null) {
      GM_setValue('api_key', nextKey.trim());
    }
    render();
  }

  if (typeof GM_registerMenuCommand === 'function') {
    GM_registerMenuCommand('设置 MangaAlpha 服务器地址与 API Key', promptConfig);
    GM_registerMenuCommand('清空 MangaAlpha 脚本配置', () => {
      GM_setValue('server_url', '');
      GM_setValue('api_key', '');
      render();
    });
  }

  const bar = document.createElement('div');
  bar.id = 'mangaalpha-bgm-bar';
  bar.style.cssText = 'display:inline-flex;flex-wrap:wrap;align-items:center;gap:6px;margin:8px 0 2px;font-size:12px;';
  titleEl.closest('h1')?.insertAdjacentElement('afterend', bar) || header.appendChild(bar);

  function makeLink(text, href, styleKind, title) {
    const a = document.createElement('a');
    a.textContent = text;
    a.href = href;
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
    if (title) a.title = title;
    const base = 'display:inline-flex;align-items:center;gap:4px;padding:3px 10px;border-radius:999px;text-decoration:none;font-weight:600;line-height:1.5;border:1px solid transparent;';
    if (styleKind === 'found') {
      a.style.cssText = base + 'background:#16a34a;color:#fff;border-color:#15803d;';
    } else if (styleKind === 'sub') {
      a.style.cssText = base + 'background:rgba(22,163,74,.12);color:#15803d;border-color:rgba(22,163,74,.35);font-weight:500;';
    } else if (styleKind === 'warn') {
      a.style.cssText = base + 'background:rgba(217,119,6,.12);color:#b45309;border-color:rgba(217,119,6,.35);';
    } else {
      a.style.cssText = base + 'background:rgba(100,116,139,.12);color:#475569;border-color:rgba(100,116,139,.3);';
    }
    return a;
  }

  function makeActionBtn(text, onClick, styleKind) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.textContent = text;
    btn.onclick = onClick;
    const base = 'display:inline-flex;align-items:center;padding:3px 10px;border-radius:999px;cursor:pointer;font-size:12px;font-weight:600;line-height:1.5;border:1px solid transparent;';
    if (styleKind === 'warn') {
      btn.style.cssText = base + 'background:rgba(217,119,6,.12);color:#b45309;border-color:rgba(217,119,6,.35);';
    } else {
      btn.style.cssText = base + 'background:rgba(100,116,139,.12);color:#475569;border-color:rgba(100,116,139,.3);';
    }
    return btn;
  }

  function render() {
    bar.textContent = '';
    const serverUrl = getServerUrl();
    const apiKey = getApiKey();

    if (!serverUrl) {
      bar.appendChild(makeActionBtn('⚙ 配置 MangaAlpha 地址', promptConfig, 'muted'));
      return;
    }

    const jumpUrl = `${serverUrl}/bgm/${encodeURIComponent(subjectId)}${pageTitle ? `?title=${encodeURIComponent(pageTitle)}` : ''}`;
    const searchUrl = `${serverUrl}/libraries?search=${encodeURIComponent(pageTitle)}`;

    if (!apiKey) {
      bar.appendChild(makeLink('在 MangaAlpha 中查看 ↗', jumpUrl, 'muted', '未配置 API Key，点击将使用浏览器当前登录态跳转'));
      bar.appendChild(makeActionBtn('配置自动探测', promptConfig, 'muted'));
      return;
    }

    const loading = document.createElement('span');
    loading.textContent = 'MangaAlpha 探测中…';
    loading.style.cssText = 'color:#64748b;padding:3px 8px;';
    bar.appendChild(loading);

    GM_xmlhttpRequest({
      method: 'GET',
      url: `${serverUrl}/api/v1/bangumi/lookup?subject_id=${encodeURIComponent(subjectId)}`,
      headers: { 'X-API-Key': apiKey, 'Accept': 'application/json' },
      timeout: 8000,
      onload(resp) {
        bar.textContent = '';
        if (resp.status === 401 || resp.status === 403) {
          bar.appendChild(makeActionBtn('MangaAlpha API Key 无效（点此更新）', promptConfig, 'warn'));
          bar.appendChild(makeLink('直接打开 ↗', jumpUrl, 'muted'));
          return;
        }
        if (resp.status < 200 || resp.status >= 300) {
          bar.appendChild(makeLink(`在 MangaAlpha 中打开 ↗（状态 ${resp.status}）`, jumpUrl, 'warn'));
          return;
        }
        let data;
        try {
          data = JSON.parse(resp.responseText || '{}');
        } catch {
          bar.appendChild(makeLink('在 MangaAlpha 中打开 ↗', jumpUrl, 'warn'));
          return;
        }
        const matches = Array.isArray(data.matches) ? data.matches : [];
        if (data.found && matches.length === 1) {
          const item = matches[0];
          const countText = item.oneshot ? '单本' : `共 ${item.booksCount || 0} 册`;
          const readText = item.booksReadCount > 0 ? ` · 已读 ${item.booksReadCount}` : '';
          bar.appendChild(makeLink(
            `✓ 已收录（${item.libraryName} · ${countText}${readText}）前往阅读 ↗`,
            `${serverUrl}${item.url}`,
            'found',
            `${item.title} (${item.libraryName})`
          ));
        } else if (data.found && matches.length > 1) {
          bar.appendChild(makeLink(
            `✓ 已收录（${matches.length} 个版本）前往查看 ↗`,
            `${serverUrl}${data.jumpUrl || `/bgm/${subjectId}`}`,
            'found',
            '点击进入优先版本并展开版本切换栏'
          ));
          matches.slice(0, 4).forEach((item) => {
            bar.appendChild(makeLink(
              `${item.libraryName} (${item.booksCount || 0}册)`,
              `${serverUrl}${item.url}`,
              'sub',
              item.title
            ));
          });
        } else {
          bar.appendChild(makeLink('MangaAlpha 未收录 · 去本库搜索 ↗', searchUrl, 'muted'));
        }
      },
      onerror() {
        bar.textContent = '';
        bar.appendChild(makeLink('在 MangaAlpha 中打开 ↗（无法直连探测）', jumpUrl, 'warn'));
      },
      ontimeout() {
        bar.textContent = '';
        bar.appendChild(makeLink('在 MangaAlpha 中打开 ↗（探测超时）', jumpUrl, 'warn'));
      },
    });
  }

  render();
})();
