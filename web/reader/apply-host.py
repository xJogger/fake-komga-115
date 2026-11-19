#!/usr/bin/env python3
"""
Deterministic host adapter patcher for upstream Suwayomi-WebUI (MPL-2.0).
Adapts the Reader module to run standalone under /reader/* backed by Komga v1 REST APIs.
"""

from __future__ import annotations

import json
import shutil
import sys
from pathlib import Path


def replace_exact(path: Path, old: str, new: str) -> None:
    text = path.read_text(encoding="utf-8")
    if old not in text:
        raise RuntimeError(f"Expected snippet not found in {path}:\n{old[:180]}")
    path.write_text(text.replace(old, new, 1), encoding="utf-8")


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("Usage: apply-host.py <host_dir> <suwayomi_src_root>")

    host_dir = Path(sys.argv[1]).resolve()
    root = Path(sys.argv[2]).resolve()

    # 1. Copy host adapter modules
    target_host_dir = root / "src" / "host"
    target_host_dir.mkdir(parents=True, exist_ok=True)
    shutil.copy2(host_dir / "host" / "KomgaHostBridge.ts", target_host_dir / "KomgaHostBridge.ts")
    shutil.copy2(host_dir / "host" / "KomgaReaderApp.tsx", root / "src" / "App.tsx")

    # 2. Relax Node engine constraint & simplify build script in package.json
    pkg_path = root / "package.json"
    pkg = json.loads(pkg_path.read_text(encoding="utf-8"))
    pkg.pop("engines", None)
    pkg["scripts"]["build"] = "tsx tools/scripts/setupEnvFiles.ts && vite build"
    pkg["scripts"]["prepare"] = "node -e ''"
    pkg_path.write_text(json.dumps(pkg, indent=2) + "\n", encoding="utf-8")

    # 3. Configure Vite for /reader/ subpath without ServiceWorker (PWA)
    vite_cfg = root / "vite.config.ts"
    vite_cfg.write_text(
        """/*
 * Copyright (C) Contributors to the Suwayomi project
 *
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

import path from 'path';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react-swc';
import { lingui } from '@lingui/vite-plugin';
import 'dotenv/config';

export default defineConfig(() => ({
    base: '/reader/',
    build: {
        outDir: 'build',
        sourcemap: false,
    },
    resolve: {
        tsconfigPaths: true,
        alias: {
            '@': path.resolve(import.meta.dirname, './src'),
        },
    },
    optimizeDeps: {
        include: ['@mui/material/Tooltip'],
    },
    plugins: [
        react({
            plugins: [['@lingui/swc-plugin', {}]],
        }),
        lingui({
            failOnCompileError: true,
        }),
    ],
}));
""",
        encoding="utf-8",
    )

    # 4. Clean index.html title, lang="zh-CN", & dark background
    idx_html = root / "index.html"
    html_text = idx_html.read_text(encoding="utf-8")
    html_text = html_text.replace('<html lang="en">', '<html lang="zh-CN">')
    html_text = html_text.replace("<title>Suwayomi</title>", "<title>阅读器 · MangaAlpha</title>")
    if "<style>" not in html_text:
        html_text = html_text.replace(
            "</head>",
            "<style>html,body,#root{background:#000;margin:0;padding:0;}</style></head>",
        )
    idx_html.write_text(html_text, encoding="utf-8")

    # 4b. Default localization to Simplified Chinese (zh-Hans) and await catalog before initial render
    i18n_ts = root / "src" / "i18n" / "index.ts"
    replace_exact(
        i18n_ts,
        "const DEFAULT_LOCALE = 'en';",
        "const DEFAULT_LOCALE = 'zh-Hans';",
    )

    iso_lang_ts = root / "src" / "lib" / "ISOLanguageUtil.ts"
    replace_exact(
        iso_lang_ts,
        "export const DEFAULT_LANGUAGE = 'en';\n\nexport const SELECTED_LANGUAGE_CODE_KEY = 'i18n:selected-language';",
        "export const DEFAULT_LANGUAGE = 'zh-Hans';\n\nexport const SELECTED_LANGUAGE_CODE_KEY = 'fk115:reader:language';",
    )
    replace_exact(
        iso_lang_ts,
        """export function detectLocale(): string {
    const storedLanguage = AppStorage.local.getItem(SELECTED_LANGUAGE_CODE_KEY);
    const preferredLanguages = getPreferredISOLanguageCodes();
    const languageCodes = [storedLanguage, ...preferredLanguages];

    const normalizedLanguageCodes = languageCodes
        .filter((code) => code !== null)
        .filter((code) => getISOLanguage(code))
        .filter((code) => i18nResources.includes(code as (typeof i18nResources)[number]));

    return normalizedLanguageCodes[0] ?? DEFAULT_LANGUAGE;
}""",
        """export function detectLocale(): string {
    const storedLanguage = AppStorage.local.getItem(SELECTED_LANGUAGE_CODE_KEY);
    if (storedLanguage && i18nResources.includes(storedLanguage as (typeof i18nResources)[number])) {
        return storedLanguage;
    }
    return DEFAULT_LANGUAGE;
}""",
    )

    entry_tsx = root / "src" / "index.tsx"
    replace_exact(
        entry_tsx,
        """initializeLocalization().catch(defaultPromiseErrorHandler('i18n', true));

const container = document.getElementById('root');
const root = createRoot(container!);
root.render(
    <StrictMode>
        <App />
    </StrictMode>,
);""",
        """const container = document.getElementById('root');
const root = createRoot(container!);
initializeLocalization()
    .catch(defaultPromiseErrorHandler('i18n', true))
    .finally(() => {
        root.render(
            <StrictMode>
                <App />
            </StrictMode>,
        );
    });""",
    )

    # 5. SubpathUtil: Router basename = '/reader', API base URL = root origin (no '/reader' suffix)
    subpath_ts = root / "src" / "lib" / "utils" / "SubpathUtil.ts"
    subpath_ts.write_text(
        """/*
 * Copyright (C) Contributors to the Suwayomi project
 *
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

export class SubpathUtil {
    public static getSubpath(): string {
        return '/reader';
    }

    public static getRouterBasename(): string | undefined {
        return '/reader';
    }

    public static getApiBaseUrl(serverUrl: string): string {
        return serverUrl;
    }

    public static getPathname(): string {
        const p = window.location.pathname;
        return p.startsWith('/reader') ? p.slice('/reader'.length) || '/' : p;
    }
}
""",
        encoding="utf-8",
    )

    # 6. AuthManager: session cookie auth is handled by fake-komga-115 HTTP middleware
    auth_ts = root / "src" / "features" / "authentication" / "AuthManager.ts"
    replace_exact(
        auth_ts,
        "    private static authInitialized: boolean = false;\n\n    private static authRequired: boolean | null = null;",
        "    private static authInitialized: boolean = true;\n\n    private static authRequired: boolean | null = false;",
    )

    # 7. GraphQLClient: route all Apollo operations through in-process KomgaHostBridge link (zero WebSocket)
    gql_client = root / "src" / "lib" / "requests" / "client" / "GraphQLClient.ts"
    replace_exact(
        gql_client,
        "import { BaseClient } from '@/lib/requests/client/BaseClient.ts';",
        "import { BaseClient } from '@/lib/requests/client/BaseClient.ts';\nimport { KomgaHostBridge } from '@/host/KomgaHostBridge.ts';",
    )
    replace_exact(
        gql_client,
        """    override reset(): void {
        super.reset();

        this.client.clearStore();
        this.client.stop();

        this.resetWsClient(false);
        this.createClient();
    }""",
        """    override reset(): void {
        super.reset();

        this.client.clearStore();
        this.client.stop();

        this.createClient();
    }""",
    )
    replace_exact(
        gql_client,
        """    protected createClient(createWsClientLazily?: boolean) {
        this.createWSClient(createWsClientLazily);
        this.client = new ApolloClient({
            cache: new InMemoryCache({ typePolicies }),
            devtools: { enabled: true },
            link: this.createLink(),
        });
    }""",
        """    protected createClient(_createWsClientLazily?: boolean) {
        this.client = new ApolloClient({
            cache: new InMemoryCache({ typePolicies }),
            devtools: { enabled: false },
            link: KomgaHostBridge.createApolloLink(),
        });
    }""",
    )

    # 8. Manga-friendly reader defaults (RTL, Double-page on wide screens, Single-page on mobile, Black canvas)
    reader_consts = root / "src" / "features" / "reader" / "settings" / "ReaderSettings.constants.tsx"
    replace_exact(
        reader_consts,
        """    pageScaleMode: ReaderPageScaleMode.ORIGINAL,
    shouldStretchPage: false,
    shouldOffsetDoubleSpreads: false,
    shouldSkipDupChapters: true,
    shouldSkipFilteredChapters: false,
    shouldShowPageNumber: true,
    isStaticNav: false,
    readingDirection: ReadingDirection.LTR,
    readingMode: ReadingMode.SINGLE_PAGE,
    exitMode: ReaderExitMode.PREVIOUS,
    backgroundColor: ReaderBackgroundColor.THEME,""",
        """    pageScaleMode:
        typeof window !== 'undefined' && window.innerWidth >= 900
            ? ReaderPageScaleMode.SCREEN
            : ReaderPageScaleMode.WIDTH,
    shouldStretchPage: false,
    shouldOffsetDoubleSpreads: false,
    shouldSkipDupChapters: true,
    shouldSkipFilteredChapters: false,
    shouldShowPageNumber: true,
    isStaticNav: false,
    readingDirection: ReadingDirection.RTL,
    readingMode:
        typeof window !== 'undefined' && window.innerWidth >= 900
            ? ReadingMode.DOUBLE_PAGE
            : ReadingMode.SINGLE_PAGE,
    exitMode: ReaderExitMode.MANGA,
    backgroundColor: ReaderBackgroundColor.BLACK,""",
    )

    # 9. ReaderService.useExit: return cleanly to /series/{seriesId} on fake-komga-115 Bookshelf
    reader_svc = root / "src" / "features" / "reader" / "services" / "ReaderService.ts"
    replace_exact(
        reader_svc,
        "import { UrlUtil } from '@/lib/UrlUtil.ts';",
        "import { UrlUtil } from '@/lib/UrlUtil.ts';\nimport { KomgaHostBridge } from '@/host/KomgaHostBridge.ts';",
    )
    replace_exact(
        reader_svc,
        """    static useExit(): () => void {
        const exitMode = useReaderSettingsStore('exitMode');
        const handleBack = useBackButton();
        const navigate = useNavigate();

        const openMangaPage = useCallback(() => {
            const { manga } = getReaderStore();

            if (!manga) {
                return () => {};
            }

            return navigate(AppRoutes.manga.path(manga.id));
        }, []);

        switch (exitMode) {
            case ReaderExitMode.PREVIOUS:
                return handleBack;
            case ReaderExitMode.MANGA:
                return openMangaPage;
            default:
                throw new Error(`Unexpected "exitMode" (${exitMode})`);
        }
    }""",
        """    static useExit(): () => void {
        return useCallback(() => {
            KomgaHostBridge.flushAllProgress();
            const { manga } = getReaderStore();
            const seriesId = manga ? KomgaHostBridge.getSeriesIdForManga(manga.id) : undefined;
            window.location.assign(seriesId ? `/series/${encodeURIComponent(seriesId)}` : '/');
        }, []);
    }""",
    )


if __name__ == "__main__":
    main()
