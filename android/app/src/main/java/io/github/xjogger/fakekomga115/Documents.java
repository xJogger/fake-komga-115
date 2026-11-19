package io.github.xjogger.fakekomga115;

import android.content.ContentResolver;
import android.content.UriPermission;
import android.database.Cursor;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import android.provider.DocumentsContract;
import android.provider.DocumentsContract.Document;

import java.io.IOException;
import java.util.LinkedHashMap;

import org.json.JSONArray;
import org.json.JSONObject;
import mobile.DocumentBackend;

final class Documents implements DocumentBackend {
    private final ContentResolver resolver;
    // Only directory walks use this cache. Root enumeration discards previous
    // results; opening an archive always resolves its current relative name.
    private final LinkedHashMap<String, String> scannedDirs = new LinkedHashMap<>();
    private static final int MAX_SCAN_DIRS = 16384;
    private static final String[] COLUMNS = { Document.COLUMN_DOCUMENT_ID,
        Document.COLUMN_DISPLAY_NAME, Document.COLUMN_MIME_TYPE,
        Document.COLUMN_SIZE, Document.COLUMN_LAST_MODIFIED };

    Documents(ContentResolver resolver) { this.resolver = resolver; }

    private Uri allowedTree(String root) throws IOException {
        Uri tree = Uri.parse(root);
        if (!"content".equals(tree.getScheme()) || !DocumentsContract.isTreeUri(tree)) {
            throw new IOException("请选择有效的 Android 目录");
        }
        for (UriPermission grant : resolver.getPersistedUriPermissions()) {
            if (grant.isReadPermission() && tree.equals(grant.getUri())) return tree;
        }
        throw new IOException("Android 目录授权已失效，请重新授权同一个目录");
    }

    private boolean isDirectory(Cursor c) { return Document.MIME_TYPE_DIR.equals(c.getString(2)); }

    private Cursor children(Uri tree, String id) throws IOException {
        Cursor c = resolver.query(DocumentsContract.buildChildDocumentsUriUsingTree(tree, id),
                COLUMNS, null, null, null);
        if (c == null) throw new IOException("Android 文件提供方未返回目录");
        if (c.getExtras().getBoolean(DocumentsContract.EXTRA_LOADING) ||
                c.getExtras().getString(DocumentsContract.EXTRA_ERROR) != null) {
            c.close();
            throw new IOException("Android 文件提供方暂时未能列出完整目录，请稍后重试");
        }
        return c;
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
            if (rel.isEmpty()) scannedDirs.keySet().removeIf(key -> key.startsWith(prefix));
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
                if (dir && id != null && name != null && !name.contains("/")) {
                    synchronized (scannedDirs) {
                        scannedDirs.put(prefix + (rel.isEmpty() ? name : rel + "/" + name), id);
                        if (scannedDirs.size() > MAX_SCAN_DIRS) {
                            scannedDirs.remove(scannedDirs.keySet().iterator().next());
                        }
                    }
                }
                out.put(new JSONObject().put("ID", id).put("Name", name)
                    .put("IsDir", dir).put("Size", c.isNull(3) ? 0L : c.getLong(3))
                    .put("MTime", c.isNull(4) ? 0L : c.getLong(4)));
            }
        } catch (SecurityException e) {
            throw new IOException("Android 目录授权已失效，请重新授权同一个目录");
        }
        return out.toString();
    }

    @Override public int open(String root, String rel) throws Exception {
        Uri tree = allowedTree(root);
        try {
            String id = resolve(tree, rel, false);
            try (ParcelFileDescriptor fd = resolver.openFileDescriptor(
                    DocumentsContract.buildDocumentUriUsingTree(tree, id), "r")) {
                if (fd == null) throw new IOException("Android 文件提供方无法打开此归档");
                return fd.detachFd();
            }
        } catch (SecurityException e) {
            throw new IOException("Android 目录授权已失效，请重新授权同一个目录");
        }
    }
}
