package io.github.xjogger.fakekomga115;

import android.content.ContentResolver;
import android.content.Context;
import android.content.UriPermission;
import android.database.Cursor;
import android.net.Uri;
import android.os.Bundle;
import android.os.ParcelFileDescriptor;
import android.provider.DocumentsContract;
import android.provider.DocumentsContract.Document;

import java.io.FileNotFoundException;
import java.io.IOException;
import java.util.LinkedHashMap;
import java.util.Map;

import org.json.JSONArray;
import org.json.JSONObject;
import mobile.DocumentBackend;

final class Documents implements DocumentBackend {
    private final Context appContext;
    private final ContentResolver resolver;
    // Directory walk cache (reset when enumerating tree root).
    private final LinkedHashMap<String, String> scannedDirs = new LinkedHashMap<>();
    // Bounded LRU cache of resolved file document IDs so page-by-page reading
    // does not re-walk the entire SAF tree on every page request.
    private final LinkedHashMap<String, String> resolvedFiles = new LinkedHashMap<String, String>(256, 0.75f, true) {
        @Override protected boolean removeEldestEntry(Map.Entry<String, String> eldest) {
            return size() > MAX_FILE_CACHE;
        }
    };
    private static final int MAX_SCAN_DIRS = 16384;
    private static final int MAX_FILE_CACHE = 4096;
    private static final int LOADING_RETRIES = 3;
    private static final String[] COLUMNS = { Document.COLUMN_DOCUMENT_ID,
        Document.COLUMN_DISPLAY_NAME, Document.COLUMN_MIME_TYPE,
        Document.COLUMN_SIZE, Document.COLUMN_LAST_MODIFIED };

    Documents(Context context, ContentResolver resolver) {
        this.appContext = context.getApplicationContext();
        this.resolver = resolver;
    }

    private Uri allowedTree(String root) throws IOException {
        Uri tree = Uri.parse(root);
        if (!"content".equals(tree.getScheme()) || !DocumentsContract.isTreeUri(tree)) {
            throw new IOException("请选择有效的 Android 目录");
        }
        for (UriPermission grant : resolver.getPersistedUriPermissions()) {
            if (grant.isReadPermission() && tree.equals(grant.getUri())) return tree;
        }
        DiagnosticsLog.append(appContext, "error", "android_saf", "permission", "PERMISSION_REVOKED",
                "persisted tree URI read permission missing");
        throw new IOException("Android 目录授权已失效，请重新授权同一个目录");
    }

    private boolean isDirectory(Cursor c) { return Document.MIME_TYPE_DIR.equals(c.getString(2)); }

    private Cursor children(Uri tree, String id) throws IOException {
        for (int attempt = 0; attempt <= LOADING_RETRIES; attempt++) {
            Cursor c = resolver.query(DocumentsContract.buildChildDocumentsUriUsingTree(tree, id),
                    COLUMNS, null, null, null);
            if (c == null) {
                DiagnosticsLog.append(appContext, "error", "android_saf", "query", "NULL_CURSOR",
                        "document provider returned null cursor");
                throw new IOException("Android 文件提供方未返回目录");
            }
            Bundle extras = c.getExtras();
            boolean loading = extras != null && extras.getBoolean(DocumentsContract.EXTRA_LOADING);
            String extraErr = extras != null ? extras.getString(DocumentsContract.EXTRA_ERROR) : null;
            if (extraErr != null) {
                c.close();
                DiagnosticsLog.append(appContext, "error", "android_saf", "query", "EXTRA_ERROR",
                        "document provider reported EXTRA_ERROR");
                throw new IOException("Android 文件提供方暂时未能列出完整目录，请稍后重试");
            }
            if (!loading) {
                return c;
            }
            c.close();
            if (attempt == LOADING_RETRIES) {
                DiagnosticsLog.append(appContext, "warn", "android_saf", "query", "EXTRA_LOADING",
                        "document provider still loading after retries");
                throw new IOException("Android 文件提供方暂时未能列出完整目录，请稍后重试");
            }
            try {
                Thread.sleep(200L * (attempt + 1));
            } catch (InterruptedException ie) {
                Thread.currentThread().interrupt();
                throw new IOException("Android 目录读取被中断");
            }
        }
        throw new IOException("Android 文件提供方未返回目录");
    }

    private String find(Uri tree, String parent, String name, boolean dir) throws IOException {
        if (name.isEmpty() || ".".equals(name) || "..".equals(name) || name.indexOf('\\') >= 0) {
            throw new IOException("无效的 Android 文件路径");
        }
        String found = null;
        try (Cursor c = children(tree, parent)) {
            while (c.moveToNext()) {
                if (name.equals(c.getString(1))) {
                    if (found != null) throw new IOException("Android 目录中存在重名条目");
                    if (dir != isDirectory(c)) throw new IOException("Android 文件类型已改变");
                    found = c.getString(0);
                }
            }
        }
        if (found == null) throw new IOException("找不到 Android 文件或目录，请重扫书库");
        return found;
    }

    private String resolve(Uri tree, String rel, boolean dir) throws IOException {
        String id = DocumentsContract.getTreeDocumentId(tree);
        if (rel.isEmpty() && dir) return id;
        String[] parts = rel.split("/", -1);
        for (int i = 0; i < parts.length; i++) {
            id = find(tree, id, parts[i], i < parts.length - 1 || dir);
        }
        return id;
    }

    @Override public String list(String root, String rel) throws Exception {
        Uri tree = allowedTree(root);
        String prefix = root + (char) 0;
        String parent;
        synchronized (scannedDirs) {
            if (rel.isEmpty()) {
                scannedDirs.keySet().removeIf(key -> key.startsWith(prefix));
                synchronized (resolvedFiles) {
                    resolvedFiles.keySet().removeIf(key -> key.startsWith(prefix));
                }
            }
            parent = scannedDirs.get(prefix + rel);
        }
        if (parent == null) parent = resolve(tree, rel, true);
        JSONArray out = new JSONArray();
        try (Cursor c = children(tree, parent)) {
            while (c.moveToNext()) {
                String id = c.getString(0);
                if (parent.equals(id) || DocumentsContract.getTreeDocumentId(tree).equals(id)) continue;
                String name = c.getString(1);
                boolean dir = isDirectory(c);
                if (id != null && name != null && !name.contains("/")) {
                    String childRel = rel.isEmpty() ? name : rel + "/" + name;
                    if (dir) {
                        synchronized (scannedDirs) {
                            scannedDirs.put(prefix + childRel, id);
                            if (scannedDirs.size() > MAX_SCAN_DIRS) {
                                scannedDirs.remove(scannedDirs.keySet().iterator().next());
                            }
                        }
                    } else {
                        synchronized (resolvedFiles) {
                            resolvedFiles.put(prefix + childRel, id);
                        }
                    }
                }
                long rawSize = c.isNull(3) ? 0L : c.getLong(3);
                long rawMTime = c.isNull(4) ? 0L : c.getLong(4);
                out.put(new JSONObject().put("ID", id).put("Name", name)
                    .put("IsDir", dir).put("Size", Math.max(0L, rawSize))
                    .put("MTime", Math.max(0L, rawMTime)));
            }
        } catch (SecurityException e) {
            DiagnosticsLog.append(appContext, "error", "android_saf", "list", "SECURITY_EXCEPTION", e.getMessage());
            throw new IOException("Android 目录授权已失效，请重新授权同一个目录");
        }
        return out.toString();
    }

    private ParcelFileDescriptor openByDocId(Uri tree, String docId) throws FileNotFoundException {
        return resolver.openFileDescriptor(DocumentsContract.buildDocumentUriUsingTree(tree, docId), "r");
    }

    @Override public int open(String root, String rel) throws Exception {
        Uri tree = allowedTree(root);
        String cacheKey = root + (char) 0 + rel;
        String cachedId;
        synchronized (resolvedFiles) {
            cachedId = resolvedFiles.get(cacheKey);
        }
        if (cachedId != null) {
            try (ParcelFileDescriptor fd = openByDocId(tree, cachedId)) {
                if (fd != null) {
                    return fd.detachFd();
                }
            } catch (FileNotFoundException | IllegalArgumentException | SecurityException ignored) {
                synchronized (resolvedFiles) {
                    resolvedFiles.remove(cacheKey);
                }
            }
        }
        try {
            String id = resolve(tree, rel, false);
            try (ParcelFileDescriptor fd = openByDocId(tree, id)) {
                if (fd == null) {
                    DiagnosticsLog.append(appContext, "error", "android_saf", "open", "NULL_FD",
                            "openFileDescriptor returned null");
                    throw new IOException("Android 文件提供方无法打开此归档");
                }
                synchronized (resolvedFiles) {
                    resolvedFiles.put(cacheKey, id);
                }
                return fd.detachFd();
            }
        } catch (SecurityException e) {
            DiagnosticsLog.append(appContext, "error", "android_saf", "open", "SECURITY_EXCEPTION", e.getMessage());
            throw new IOException("Android 目录授权已失效，请重新授权同一个目录");
        } catch (IOException e) {
            DiagnosticsLog.append(appContext, "error", "android_saf", "open", "IO_ERROR", e.getMessage());
            throw e;
        }
    }
}
