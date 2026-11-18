/*
 * Copyright (C) Contributors to the Suwayomi project
 * Copyright (C) fake-komga-115 contributors
 *
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

import CssBaseline from '@mui/material/CssBaseline';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';
import type { PropsWithChildren } from 'react';
import React, { useEffect, useState } from 'react';
import { Route, Routes, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { AwaitableComponent } from 'awaitable-component';
import { loadable } from 'react-lazily/loadable';
import { AppContext } from '@/base/contexts/AppContext.tsx';
import { lazyLoadFallback } from '@/base/utils/LazyLoad.tsx';
import { ErrorBoundary } from '@/base/components/feedback/ErrorBoundary.tsx';
import { LoadingPlaceholder } from '@/base/components/feedback/LoadingPlaceholder.tsx';
import { ReactRouter } from '@/lib/react-router/ReactRouter.ts';
import { requestManager } from '@/lib/requests/RequestManager.ts';
import { useNavBarContext } from '@/features/navigation-bar/NavbarContext.tsx';
import { ReaderResumeMode } from '@/features/reader/Reader.types.ts';
import { KomgaHostBridge } from '@/host/KomgaHostBridge.ts';

const { Reader } = loadable(() => import('@/features/reader/screens/Reader.tsx'), lazyLoadFallback);

const ReactRouterSetter = () => {
    const navigate = useNavigate();
    useEffect(() => {
        ReactRouter.setNavigateFn(navigate);
    }, [navigate]);
    return null;
};

const InitializeGuard = ({ children }: PropsWithChildren) => {
    const [ready, setReady] = useState(false);

    useEffect(() => {
        Promise.allSettled([
            requestManager.getGlobalMeta().response,
            requestManager.getServerSettings().response,
        ]).finally(() => setReady(true));
    }, []);

    if (!ready) {
        return (
            <Box sx={{ height: '100vh', width: '100vw', display: 'grid', placeItems: 'center', bgcolor: '#000' }}>
                <LoadingPlaceholder />
            </Box>
        );
    }
    return children;
};

const ReaderOverlayHost = () => {
    const { override } = useNavBarContext();
    if (!override.status) return null;
    return override.value;
};

const KomgaEntryRedirect: React.FC = () => {
    const { seriesId = '', bookId = '' } = useParams<{ seriesId: string; bookId: string }>();
    const [searchParams] = useSearchParams();
    const navigate = useNavigate();
    const [errText, setErrText] = useState<string | null>(null);

    useEffect(() => {
        let cancelled = false;
        const pageRaw = searchParams.get('page');
        const pageNum = pageRaw ? parseInt(pageRaw, 10) : undefined;
        KomgaHostBridge.prepareEntry(
            seriesId,
            bookId,
            pageNum && !Number.isNaN(pageNum) && pageNum >= 1 ? pageNum : undefined,
        )
            .then(({ mangaId, sourceOrder }) => {
                if (cancelled) return;
                navigate(`/manga/${mangaId}/chapter/${sourceOrder}`, {
                    replace: true,
                    state: {
                        resumeMode: ReaderResumeMode.LAST_READ,
                        updateInitialChapter: true,
                    },
                });
            })
            .catch((e) => {
                if (!cancelled) {
                    setErrText(e instanceof Error ? e.message : String(e));
                }
            });
        return () => {
            cancelled = true;
        };
    }, [seriesId, bookId, searchParams, navigate]);

    if (errText) {
        return (
            <Stack
                spacing={2}
                sx={{
                    height: '100vh',
                    width: '100vw',
                    alignItems: 'center',
                    justifyContent: 'center',
                    bgcolor: '#0b0d12',
                    color: '#fff',
                    p: 3,
                }}
            >
                <Typography variant="h6">无法打开阅读器</Typography>
                <Typography variant="body2" sx={{ color: 'rgba(255,255,255,0.7)' }}>
                    {errText}
                </Typography>
                <Button variant="contained" onClick={() => window.location.assign(seriesId ? `/series/${encodeURIComponent(seriesId)}` : '/')}>
                    返回书架
                </Button>
            </Stack>
        );
    }

    return (
        <Box sx={{ height: '100vh', width: '100vw', display: 'grid', placeItems: 'center', bgcolor: '#000' }}>
            <LoadingPlaceholder />
        </Box>
    );
};

const ExitToSeriesPage: React.FC = () => {
    const { id } = useParams<{ id: string }>();
    useEffect(() => {
        KomgaHostBridge.flushAllProgress();
        const mangaId = Number(id);
        const seriesId = !Number.isNaN(mangaId) ? KomgaHostBridge.getSeriesIdForManga(mangaId) : undefined;
        window.location.replace(seriesId ? `/series/${encodeURIComponent(seriesId)}` : '/');
    }, [id]);
    return null;
};

const ExitToHome: React.FC = () => {
    useEffect(() => {
        KomgaHostBridge.flushAllProgress();
        window.location.replace('/');
    }, []);
    return null;
};

export const App: React.FC = () => (
    <AppContext>
        <AwaitableComponent.Root />
        <ReactRouterSetter />
        <CssBaseline enableColorScheme />
        <InitializeGuard>
            <Box sx={{ display: 'flex' }}>
                <Box sx={{ flexShrink: 0, position: 'relative', height: '100vh' }}>
                    <ReaderOverlayHost />
                </Box>
                <ErrorBoundary>
                    <Routes>
                        <Route path="/series/:seriesId/book/:bookId/*" element={<KomgaEntryRedirect />} />
                        <Route path="/manga/:mangaId/chapter/:chapterSourceOrder/*" element={<Reader />} />
                        <Route path="/manga/:id/*" element={<ExitToSeriesPage />} />
                        <Route path="*" element={<ExitToHome />} />
                    </Routes>
                </ErrorBoundary>
            </Box>
        </InitializeGuard>
    </AppContext>
);
