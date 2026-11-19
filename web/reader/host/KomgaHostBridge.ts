/*
 * Copyright (C) Contributors to the Suwayomi project
 * Copyright (C) fake-komga-115 contributors
 *
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

import { ApolloLink } from '@apollo/client';
import { from } from 'rxjs';

interface KomgaSeriesDTO {
    id: string;
    name: string;
    booksCount: number;
    booksUnreadCount?: number;
    metadata?: {
        title?: string;
        summary?: string;
        status?: string;
        readingDirection?: string;
        genres?: string[];
        publisher?: string;
    };
    booksMetadata?: {
        authors?: { name: string; role: string }[];
    };
}

interface KomgaBookDTO {
    id: string;
    seriesId: string;
    seriesTitle?: string;
    name: string;
    number: number;
    fileLastModified?: string;
    media?: {
        pagesCount?: number;
    };
    metadata?: {
        title?: string;
        numberSort?: number;
    };
    readProgress?: {
        page?: number | null;
        completed?: boolean;
        readDate?: string | null;
    } | null;
}

interface ChapterRecord {
    chapterId: number;
    mangaId: number;
    seriesId: string;
    bookId: string;
    sourceOrder: number;
    chapterNumber: number;
    name: string;
    uploadDate: string;
    pageCount: number;
    isRead: boolean;
    lastPageRead: number;
    isBookmarked: boolean;
}

interface SeriesBundle {
    mangaId: number;
    seriesId: string;
    series: KomgaSeriesDTO;
    chaptersDesc: ChapterRecord[];
}

const SERIES_MAP_KEY = 'fk115_reader_series_map_v2';
const GLOBAL_META_KEY = 'fk115_reader_global_meta_v2';
const SERIES_META_PREFIX = 'fk115_reader_series_meta_v2:';

function readJSONStorage<T>(key: string, fallback: T): T {
    try {
        const raw = localStorage.getItem(key);
        if (!raw) return fallback;
        return JSON.parse(raw) as T;
    } catch {
        return fallback;
    }
}

function writeJSONStorage(key: string, value: unknown): void {
    try {
        localStorage.setItem(key, JSON.stringify(value));
    } catch {
        // Ignore quota errors
    }
}

async function komgaFetch<T>(path: string, init?: RequestInit): Promise<T> {
    const headers = new Headers(init?.headers ?? {});
    headers.set('X-FK115-UI', '1');
    if (init?.body !== undefined && !headers.has('Content-Type')) {
        headers.set('Content-Type', 'application/json');
    }
    const resp = await fetch(path, {
        ...init,
        credentials: 'same-origin',
        headers,
    });
    if (resp.status === 401) {
        const redirect = encodeURIComponent(window.location.pathname + window.location.search);
        window.location.assign(`/login?redirect=${redirect}`);
        throw new Error('Authentication required');
    }
    if (resp.status === 204) {
        return undefined as T;
    }
    if (!resp.ok) {
        let msg = `HTTP ${resp.status}`;
        try {
            const errJson = await resp.json();
            if (errJson?.message) msg = errJson.message;
        } catch {
            // ignore
        }
        throw new Error(msg);
    }
    return (await resp.json()) as T;
}

class KomgaBridgeImpl {
    private seriesToMangaId = new Map<string, number>();

    private mangaIdToSeries = new Map<number, string>();

    private nextMangaId = 1;

    private nextChapterId = 1;

    private bundles = new Map<number, SeriesBundle>();

    private chaptersById = new Map<number, ChapterRecord>();

    private bookKeyToChapterId = new Map<string, number>();

    private pendingProgressTimers = new Map<string, ReturnType<typeof setTimeout>>();

    private pendingProgressPayloads = new Map<string, { page: number; completed: boolean }>();

    private clientSettingsLoaded = false;

    constructor() {
        const saved = readJSONStorage<Record<string, number>>(SERIES_MAP_KEY, {});
        Object.entries(saved).forEach(([sid, mid]) => {
            if (typeof mid === 'number' && mid > 0) {
                this.seriesToMangaId.set(sid, mid);
                this.mangaIdToSeries.set(mid, sid);
                if (mid >= this.nextMangaId) {
                    this.nextMangaId = mid + 1;
                }
            }
        });

        window.addEventListener('pagehide', () => this.flushAllProgress());
        document.addEventListener('visibilitychange', () => {
            if (document.hidden) {
                this.flushAllProgress();
            }
        });
    }

    private getOrCreateMangaId(seriesId: string): number {
        const existing = this.seriesToMangaId.get(seriesId);
        if (existing !== undefined) return existing;
        const assigned = this.nextMangaId++;
        this.seriesToMangaId.set(seriesId, assigned);
        this.mangaIdToSeries.set(assigned, seriesId);
        const obj: Record<string, number> = {};
        this.seriesToMangaId.forEach((v, k) => {
            obj[k] = v;
        });
        writeJSONStorage(SERIES_MAP_KEY, obj);
        return assigned;
    }

    public getSeriesIdForManga(mangaId: number): string | undefined {
        return this.mangaIdToSeries.get(mangaId);
    }

    private async ensureClientSettingsSynced(): Promise<void> {
        if (this.clientSettingsLoaded) return;
        this.clientSettingsLoaded = true;
        try {
            const remote = await komgaFetch<Record<string, { value?: string }>>('/api/v1/client-settings/user/list');
            const globalEntry = remote?.['webui.reader.globalMeta']?.value;
            if (globalEntry) {
                const parsed = JSON.parse(globalEntry) as Record<string, string>;
                const local = readJSONStorage<Record<string, string>>(GLOBAL_META_KEY, {});
                writeJSONStorage(GLOBAL_META_KEY, { ...parsed, ...local });
            }
        } catch {
            // Non-fatal
        }
    }

    private syncClientSetting(key: string, metaMap: Record<string, string>): void {
        const serialized = JSON.stringify(metaMap);
        if (serialized.length > 7800) return;
        komgaFetch('/api/v1/client-settings/user', {
            method: 'PATCH',
            body: JSON.stringify({
                [key]: {
                    value: serialized,
                    allowUnauthenticated: false,
                },
            }),
        }).catch(() => {});
    }

    public async prepareEntry(
        seriesId: string,
        bookId: string,
        pageOneBased?: number,
    ): Promise<{ mangaId: number; sourceOrder: number }> {
        const bundle = await this.loadSeriesBundle(seriesId, true);
        const found = bundle.chaptersDesc.find((c) => c.bookId === bookId) ?? bundle.chaptersDesc[bundle.chaptersDesc.length - 1];
        if (!found) {
            throw new Error('该系列下没有可阅读的漫画卷');
        }
        if (pageOneBased !== undefined && pageOneBased >= 1) {
            found.lastPageRead = Math.max(0, pageOneBased - 1);
        }
        return { mangaId: bundle.mangaId, sourceOrder: found.sourceOrder };
    }

    private async loadSeriesBundle(seriesId: string, forceRefresh = false): Promise<SeriesBundle> {
        const mangaId = this.getOrCreateMangaId(seriesId);
        if (!forceRefresh) {
            const cached = this.bundles.get(mangaId);
            if (cached) return cached;
        }

        const [series, booksResp] = await Promise.all([
            komgaFetch<KomgaSeriesDTO>(`/api/v1/series/${encodeURIComponent(seriesId)}`),
            komgaFetch<{ content?: KomgaBookDTO[] }>(
                `/api/v1/series/${encodeURIComponent(seriesId)}/books?unpaged=true&sort=metadata.numberSort,asc`,
            ),
        ]);

        const booksAsc = booksResp.content ?? [];
        const chaptersAsc: ChapterRecord[] = booksAsc.map((b, idx) => {
            const sourceOrder = idx + 1;
            const bookKey = `${seriesId}::${b.id}`;
            let chapterId = this.bookKeyToChapterId.get(bookKey);
            if (chapterId === undefined) {
                chapterId = this.nextChapterId++;
                this.bookKeyToChapterId.set(bookKey, chapterId);
            }
            const rp = b.readProgress;
            const isRead = Boolean(rp?.completed);
            const lastPageRead = rp?.page && rp.page > 0 ? Math.max(0, rp.page - 1) : 0;
            const modifiedMs = b.fileLastModified ? Date.parse(b.fileLastModified) : Date.now();
            const rec: ChapterRecord = {
                chapterId,
                mangaId,
                seriesId,
                bookId: b.id,
                sourceOrder,
                chapterNumber: sourceOrder,
                name: b.metadata?.title || b.name,
                uploadDate: String(Number.isNaN(modifiedMs) ? Date.now() : modifiedMs),
                pageCount: b.media?.pagesCount ?? 0,
                isRead,
                lastPageRead,
                isBookmarked: false,
            };
            this.chaptersById.set(chapterId, rec);
            return rec;
        });

        const chaptersDesc = [...chaptersAsc].reverse();
        const bundle: SeriesBundle = {
            mangaId,
            seriesId,
            series,
            chaptersDesc,
        };
        this.bundles.set(mangaId, bundle);
        return bundle;
    }

    private async ensureBundleByMangaId(mangaId: number): Promise<SeriesBundle> {
        const existing = this.bundles.get(mangaId);
        if (existing) return existing;
        const seriesId = this.mangaIdToSeries.get(mangaId);
        if (!seriesId) {
            throw new Error('未找到对应的漫画系列上下文，请从书架重新打开');
        }
        return this.loadSeriesBundle(seriesId, false);
    }

    private scheduleProgressSync(bookId: string, pageOneBased: number, completed: boolean): void {
        const prevTimer = this.pendingProgressTimers.get(bookId);
        if (prevTimer) {
            clearTimeout(prevTimer);
            this.pendingProgressTimers.delete(bookId);
        }
        const payload = { page: Math.max(1, pageOneBased), completed };
        if (completed) {
            this.pendingProgressPayloads.delete(bookId);
            komgaFetch(`/api/v1/books/${encodeURIComponent(bookId)}/read-progress`, {
                method: 'PATCH',
                body: JSON.stringify(payload),
                keepalive: true,
            }).catch(() => {});
            return;
        }
        this.pendingProgressPayloads.set(bookId, payload);
        const timer = setTimeout(() => {
            this.pendingProgressTimers.delete(bookId);
            const item = this.pendingProgressPayloads.get(bookId);
            if (!item) return;
            this.pendingProgressPayloads.delete(bookId);
            komgaFetch(`/api/v1/books/${encodeURIComponent(bookId)}/read-progress`, {
                method: 'PATCH',
                body: JSON.stringify(item),
                keepalive: true,
            }).catch(() => {});
        }, 350);
        this.pendingProgressTimers.set(bookId, timer);
    }

    public flushAllProgress(): void {
        this.pendingProgressTimers.forEach((t) => clearTimeout(t));
        this.pendingProgressTimers.clear();
        this.pendingProgressPayloads.forEach((payload, bookId) => {
            fetch(`/api/v1/books/${encodeURIComponent(bookId)}/read-progress`, {
                method: 'PATCH',
                credentials: 'same-origin',
                keepalive: true,
                headers: {
                    'Content-Type': 'application/json',
                    'X-FK115-UI': '1',
                },
                body: JSON.stringify(payload),
            }).catch(() => {});
        });
        this.pendingProgressPayloads.clear();
    }

    private buildMangaGql(bundle: SeriesBundle) {
        const s = bundle.series;
        const metaMap = readJSONStorage<Record<string, string>>(`${SERIES_META_PREFIX}${bundle.seriesId}`, {});
        const metaNodes = Object.entries(metaMap).map(([key, value]) => ({
            __typename: 'MangaMetaType',
            mangaId: bundle.mangaId,
            key,
            value,
        }));
        const authors = (s.booksMetadata?.authors ?? []).map((a) => a.name).join(', ');
        const unreadCount = bundle.chaptersDesc.filter((c) => !c.isRead).length;
        return {
            __typename: 'MangaType',
            id: bundle.mangaId,
            title: s.metadata?.title || s.name,
            thumbnailUrl: `/api/v1/series/${encodeURIComponent(bundle.seriesId)}/thumbnail`,
            thumbnailUrlLastFetched: 0,
            inLibrary: true,
            initialized: true,
            sourceId: 'komga-local',
            genre: s.metadata?.genres ?? [],
            status: s.metadata?.status || 'ONGOING',
            author: authors,
            artist: authors,
            description: s.metadata?.summary ?? '',
            unreadCount,
            downloadCount: bundle.chaptersDesc.length,
            bookmarkCount: 0,
            hasDuplicateChapters: false,
            lastFetchedAt: '0',
            inLibraryAt: '0',
            firstUnreadChapter: null,
            lastReadChapter: null,
            latestReadChapter: null,
            latestFetchedChapter: null,
            latestUploadedChapter: null,
            highestNumberedChapter: null,
            source: {
                __typename: 'SourceType',
                id: 'komga-local',
                name: 'fake-komga-115',
                displayName: 'MangaAlpha',
                lang: 'zh',
                iconUrl: '',
            },
            meta: metaNodes,
            chapters: {
                __typename: 'ChapterNodeList',
                totalCount: bundle.chaptersDesc.length,
            },
            trackRecords: {
                __typename: 'TrackRecordNodeList',
                totalCount: 0,
                nodes: [],
            },
        };
    }

    private buildChapterGql(c: ChapterRecord) {
        return {
            __typename: 'ChapterType',
            id: c.chapterId,
            name: c.name,
            mangaId: c.mangaId,
            scanlator: '',
            realUrl: `/series/${encodeURIComponent(c.seriesId)}`,
            sourceOrder: c.sourceOrder,
            chapterNumber: c.chapterNumber,
            isRead: c.isRead,
            isDownloaded: true,
            isBookmarked: c.isBookmarked,
            uploadDate: c.uploadDate,
            fetchedAt: c.uploadDate,
            lastReadAt: '0',
            lastPageRead: c.lastPageRead,
            pageCount: c.pageCount,
        };
    }

    private async executeOperation(operationName: string, variables: Record<string, any>): Promise<Record<string, any>> {
        switch (operationName) {
            case 'GET_GLOBAL_METADATAS': {
                await this.ensureClientSettingsSynced();
                const globalMap = readJSONStorage<Record<string, string>>(GLOBAL_META_KEY, {});
                const nodes = Object.entries(globalMap).map(([key, value]) => ({
                    __typename: 'GlobalMetaType',
                    key,
                    value,
                }));
                return {
                    metas: {
                        __typename: 'GlobalMetaNodeList',
                        nodes,
                        pageInfo: {
                            __typename: 'PageInfo',
                            endCursor: null,
                            hasNextPage: false,
                            hasPreviousPage: false,
                            startCursor: null,
                        },
                        totalCount: nodes.length,
                    },
                };
            }

            case 'GET_GLOBAL_METADATA': {
                const globalMap = readJSONStorage<Record<string, string>>(GLOBAL_META_KEY, {});
                const key = String(variables?.key ?? '');
                return {
                    meta:
                        key in globalMap
                            ? { __typename: 'GlobalMetaType', key, value: globalMap[key] }
                            : null,
                };
            }

            case 'UPDATE_GLOBAL_METADATA': {
                const globalMap = readJSONStorage<Record<string, string>>(GLOBAL_META_KEY, {});
                const applyDeletes = (keys?: string[]) => {
                    (keys ?? []).forEach((k) => {
                        delete globalMap[k];
                    });
                };
                const applySets = (metas?: { key: string; value: string }[]) => {
                    (metas ?? []).forEach((m) => {
                        globalMap[m.key] = m.value;
                    });
                };
                if (variables?.hasPreUpdateDeletions) applyDeletes(variables.preUpdateDeleteInput?.keys);
                if (variables?.hasUpdates) applySets(variables.updateInput?.metas);
                if (variables?.hasPostUpdateDeletions) applyDeletes(variables.postUpdateDeleteInput?.keys);
                if (variables?.isMigration) applySets(variables.migrateInput?.metas);
                writeJSONStorage(GLOBAL_META_KEY, globalMap);
                this.syncClientSetting('webui.reader.globalMeta', globalMap);

                const nodes = Object.entries(globalMap).map(([key, value]) => ({
                    __typename: 'GlobalMetaType',
                    key,
                    value,
                }));
                return {
                    ...(variables?.hasPreUpdateDeletions ? { preUpdateDeletedMeta: { __typename: 'DeleteGlobalMetasPayload', metas: nodes } } : {}),
                    ...(variables?.hasUpdates ? { updatedMeta: { __typename: 'SetGlobalMetasPayload', metas: nodes } } : {}),
                    ...(variables?.hasPostUpdateDeletions ? { postUpdateDeletedMeta: { __typename: 'DeleteGlobalMetasPayload', metas: nodes } } : {}),
                    ...(variables?.isMigration ? { migrationMeta: { __typename: 'SetGlobalMetasPayload', metas: nodes } } : {}),
                };
            }

            case 'GET_SERVER_SETTINGS': {
                return {
                    settings: {
                        __typename: 'SettingsType',
                        ip: '127.0.0.1',
                        port: 25600,
                        socksProxyEnabled: false,
                        socksProxyVersion: 5,
                        socksProxyHost: '',
                        socksProxyPort: '',
                        socksProxyUsername: '',
                        socksProxyPassword: '',
                        webUIFlavor: 'WebUI',
                        initialOpenInBrowserEnabled: false,
                        webUIInterface: 'BROWSER',
                        electronPath: '',
                        webUIChannel: 'STABLE',
                        webUIUpdateCheckInterval: 0,
                        downloadAsCbz: false,
                        downloadsPath: '',
                        autoDownloadNewChapters: false,
                        excludeEntryWithUnreadChapters: false,
                        autoDownloadNewChaptersLimit: 0,
                        autoDownloadIgnoreReUploads: false,
                        downloadConversions: [],
                        serveConversions: [],
                        maxSourcesInParallel: 1,
                        excludeUnreadChapters: false,
                        excludeNotStarted: false,
                        excludeCompleted: false,
                        globalUpdateInterval: 0,
                        updateMangas: false,
                        authMode: 'NONE',
                        authPassword: '',
                        authUsername: '',
                        jwtAudience: '',
                        jwtTokenExpiry: '0',
                        jwtRefreshExpiry: '0',
                        debugLogsEnabled: false,
                        systemTrayEnabled: false,
                        maxLogFileSize: '',
                        maxLogFiles: 0,
                        maxLogFolderSize: '',
                        backupPath: '',
                        backupTime: '',
                        backupInterval: 0,
                        backupTTL: 0,
                        autoBackupIncludeCategories: false,
                        autoBackupIncludeChapters: false,
                        autoBackupIncludeClientData: false,
                        autoBackupIncludeHistory: false,
                        autoBackupIncludeManga: false,
                        autoBackupIncludeServerSettings: false,
                        autoBackupIncludeTracking: false,
                        localSourcePath: '',
                        flareSolverrEnabled: false,
                        flareSolverrUrl: '',
                        flareSolverrTimeout: 0,
                        flareSolverrSessionName: '',
                        flareSolverrSessionTtl: 0,
                        flareSolverrAsResponseFallback: false,
                        opdsUseBinaryFileSizes: false,
                        opdsItemsPerPage: 50,
                        opdsEnablePageReadProgress: true,
                        opdsMarkAsReadOnDownload: false,
                        opdsShowOnlyUnreadChapters: false,
                        opdsShowOnlyDownloadedChapters: false,
                        opdsChapterSortOrder: 'ASC',
                        opdsCbzMimetype: 'ONE',
                        opdsSkipChapterMetadataFeed: false,
                        koreaderSyncChecksumMethod: 'BINARY',
                        koreaderSyncStrategyBackward: 'DISABLED',
                        koreaderSyncStrategyForward: 'DISABLED',
                        koreaderSyncPercentageTolerance: 0,
                        databaseType: 'H2',
                        databaseUrl: '',
                        databaseUsername: '',
                        databasePassword: '',
                        useHikariConnectionPool: false,
                        kcefEnabled: false,
                        syncDataCategories: false,
                        syncDataChapters: false,
                        syncDataHistory: false,
                        syncDataManga: false,
                        syncDataTracking: false,
                        syncInterval: 0,
                        syncYomiApiKey: '',
                        syncYomiEnabled: false,
                        syncYomiHost: '',
                    },
                };
            }

            case 'GET_MANGA_READER':
            case 'GET_MANGA_SCREEN':
            case 'GET_MANGA_META': {
                const mangaId = Number(variables?.id);
                const bundle = await this.ensureBundleByMangaId(mangaId);
                return {
                    manga: this.buildMangaGql(bundle),
                };
            }

            case 'UPDATE_MANGA_METADATA': {
                const firstMangaId =
                    variables?.updateInput?.metas?.[0]?.mangaId ??
                    variables?.preUpdateDeleteInput?.mangaId ??
                    variables?.postUpdateDeleteInput?.mangaId;
                const mangaId = Number(firstMangaId);
                const seriesId = this.mangaIdToSeries.get(mangaId) ?? String(mangaId);
                const storageKey = `${SERIES_META_PREFIX}${seriesId}`;
                const metaMap = readJSONStorage<Record<string, string>>(storageKey, {});
                const applyDeletes = (keys?: string[]) => {
                    (keys ?? []).forEach((k) => {
                        delete metaMap[k];
                    });
                };
                const applySets = (metas?: { key: string; value: string }[]) => {
                    (metas ?? []).forEach((m) => {
                        metaMap[m.key] = m.value;
                    });
                };
                if (variables?.hasPreUpdateDeletions) applyDeletes(variables.preUpdateDeleteInput?.keys);
                if (variables?.hasUpdates) applySets(variables.updateInput?.metas);
                if (variables?.hasPostUpdateDeletions) applyDeletes(variables.postUpdateDeleteInput?.keys);
                if (variables?.isMigration) applySets(variables.migrateInput?.metas);
                writeJSONStorage(storageKey, metaMap);
                this.syncClientSetting(`webui.reader.series.${seriesId}`, metaMap);

                const nodes = Object.entries(metaMap).map(([key, value]) => ({
                    __typename: 'MangaMetaType',
                    mangaId,
                    key,
                    value,
                }));
                return {
                    ...(variables?.hasPreUpdateDeletions ? { preUpdateDeletedMeta: { __typename: 'DeleteMangaMetasPayload', metas: nodes } } : {}),
                    ...(variables?.hasUpdates ? { updatedMeta: { __typename: 'SetMangaMetasPayload', metas: nodes } } : {}),
                    ...(variables?.hasPostUpdateDeletions ? { postUpdateDeletedMeta: { __typename: 'DeleteMangaMetasPayload', metas: nodes } } : {}),
                    ...(variables?.isMigration ? { migrationMeta: { __typename: 'SetMangaMetasPayload', metas: nodes } } : {}),
                };
            }

            case 'GET_CHAPTERS_READER':
            case 'GET_CHAPTERS_MANGA': {
                const mangaId = Number(variables?.condition?.mangaId ?? variables?.mangaId);
                const bundle = await this.ensureBundleByMangaId(mangaId);
                const nodes = bundle.chaptersDesc.map((c) => this.buildChapterGql(c));
                return {
                    chapters: {
                        __typename: 'ChapterNodeList',
                        nodes,
                        pageInfo: {
                            __typename: 'PageInfo',
                            endCursor: null,
                            hasNextPage: false,
                            hasPreviousPage: false,
                            startCursor: null,
                        },
                        totalCount: nodes.length,
                    },
                };
            }

            case 'GET_CHAPTER_PAGES_FETCH': {
                const chapterId = Number(variables?.input?.chapterId);
                const chap = this.chaptersById.get(chapterId);
                if (!chap) {
                    throw new Error(`未找到章节缓存 (#${chapterId})`);
                }
                const komgaPages = await komgaFetch<{ number: number }[]>(
                    `/api/v1/books/${encodeURIComponent(chap.bookId)}/pages`,
                );
                const count = komgaPages.length;
                chap.pageCount = count;
                const pages = komgaPages.map(
                    (p) => `/api/v1/books/${encodeURIComponent(chap.bookId)}/pages/${p.number}`,
                );
                const bundle = this.bundles.get(chap.mangaId);
                return {
                    fetchChapterPages: {
                        __typename: 'FetchChapterPagesPayload',
                        chapter: {
                            __typename: 'ChapterType',
                            id: chap.chapterId,
                            pageCount: count,
                            isDownloaded: true,
                            manga: {
                                __typename: 'MangaType',
                                id: chap.mangaId,
                                downloadCount: bundle?.chaptersDesc.length ?? 1,
                            },
                        },
                        pages,
                    },
                };
            }

            case 'UPDATE_CHAPTER':
            case 'UPDATE_CHAPTERS': {
                const isPlural = operationName === 'UPDATE_CHAPTERS';
                const ids: number[] = isPlural
                    ? (variables?.input?.ids ?? []).map(Number)
                    : [Number(variables?.input?.id)];
                const patch = variables?.input?.patch ?? {};
                const updatedChapters = ids
                    .map((id) => this.chaptersById.get(id))
                    .filter((c): c is ChapterRecord => Boolean(c))
                    .map((c) => {
                        if (typeof patch.lastPageRead === 'number') {
                            c.lastPageRead = Math.max(0, patch.lastPageRead);
                        }
                        if (typeof patch.isRead === 'boolean') {
                            c.isRead = patch.isRead;
                        }
                        if (typeof patch.isBookmarked === 'boolean') {
                            c.isBookmarked = patch.isBookmarked;
                        }

                        if (patch.isRead === true) {
                            const finalPage = c.pageCount > 0 ? c.pageCount : c.lastPageRead + 1;
                            this.scheduleProgressSync(c.bookId, finalPage, true);
                        } else if (patch.isRead === false && patch.lastPageRead === undefined) {
                            komgaFetch(`/api/v1/books/${encodeURIComponent(c.bookId)}/read-progress`, {
                                method: 'DELETE',
                            }).catch(() => {});
                        } else if (typeof patch.lastPageRead === 'number') {
                            this.scheduleProgressSync(c.bookId, c.lastPageRead + 1, c.isRead);
                        }

                        const bundle = this.bundles.get(c.mangaId);
                        const unreadCount = bundle ? bundle.chaptersDesc.filter((x) => !x.isRead).length : 0;
                        return {
                            __typename: 'ChapterType',
                            id: c.chapterId,
                            isBookmarked: c.isBookmarked,
                            isRead: c.isRead,
                            lastReadAt: String(Math.floor(Date.now() / 1000)),
                            lastPageRead: c.lastPageRead,
                            manga: {
                                __typename: 'MangaType',
                                id: c.mangaId,
                                unreadCount,
                                bookmarkCount: 0,
                                lastReadChapter: { __typename: 'ChapterType', id: c.chapterId },
                                latestReadChapter: { __typename: 'ChapterType', id: c.chapterId },
                                firstUnreadChapter: null,
                            },
                        };
                    });

                if (isPlural) {
                    return {
                        updateChapters: {
                            __typename: 'UpdateChaptersPayload',
                            chapters: updatedChapters,
                        },
                    };
                }
                return {
                    updateChapter: {
                        __typename: 'UpdateChapterPayload',
                        chapter: updatedChapters[0] ?? null,
                    },
                };
            }

            default:
                return {};
        }
    }

    public createApolloLink(): ApolloLink {
        return new ApolloLink((operation) =>
            from(
                this.executeOperation(operation.operationName ?? '', operation.variables ?? {}).then((data) => ({
                    data,
                })),
            ),
        );
    }
}

export const KomgaHostBridge = new KomgaBridgeImpl();
