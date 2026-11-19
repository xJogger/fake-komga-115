package io.github.xjogger.fakekomga115;

import android.Manifest;
import android.app.Activity;
import android.app.AlertDialog;
import android.content.ActivityNotFoundException;
import android.content.ComponentName;
import android.content.Context;
import android.content.Intent;
import android.content.ServiceConnection;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.IBinder;
import android.view.WindowInsets;
import android.view.View;
import android.widget.FrameLayout;
import android.graphics.Insets;
import android.webkit.CookieManager;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONObject;
import mobile.Instance;

public final class MainActivity extends Activity {
    private static final int FOLDER = 11;
    private WebView web;
    private TextView status;
    private FrameLayout frame;
    private ReaderService.LocalBinder service;
    private boolean bound;
    private volatile String origin = "";
    private String pendingFolder;

    private final ReaderService.Observer observer = this::update;
    private final ServiceConnection connection = new ServiceConnection() {
        @Override public void onServiceConnected(ComponentName name, IBinder binder) {
            service = (ReaderService.LocalBinder) binder;
            service.watch(observer);
        }
        @Override public void onServiceDisconnected(ComponentName name) {
            service = null;
            update(null, "本机书库已停止，请重新打开应用");
        }
    };

    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        status = new TextView(this);
        status.setPadding(48, 72, 48, 24);
        status.setText("正在启动本机书库…");
        frame = new FrameLayout(this);
        frame.addView(status);
        if (Build.VERSION.SDK_INT >= 30) {
            getWindow().setDecorFitsSystemWindows(false);
            frame.setOnApplyWindowInsetsListener((view, insets) -> {
                Insets bars = insets.getInsets(WindowInsets.Type.systemBars()
                    | WindowInsets.Type.displayCutout() | WindowInsets.Type.ime());
                frame.setPadding(bars.left, bars.top, bars.right, bars.bottom);
                return WindowInsets.CONSUMED;
            });
        }
        setContentView(frame);
        Intent intent = new Intent(this, ReaderService.class);
        startForegroundService(intent);
        bound = bindService(intent, connection, Context.BIND_AUTO_CREATE);
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED && !getPreferences(0).getBoolean("notifications-asked", false)) {
            getPreferences(0).edit().putBoolean("notifications-asked", true).apply();
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 12);
        }
    }

    private void update(Instance instance, String message) {
        if (instance == null) {
            status.setText(message);
            if (web != null) {
                origin = "";
                show(status);
                web.destroy();
                web = null;
            }
            return;
        }
        if (web != null && origin.equals(instance.origin())) return;
        origin = instance.origin();
        final String readyOrigin = origin;
        web = new WebView(this);
        web.getSettings().setJavaScriptEnabled(true);
        web.getSettings().setDomStorageEnabled(true);
        web.getSettings().setAllowFileAccess(false);
        web.getSettings().setAllowContentAccess(false);
        web.getSettings().setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        web.getSettings().setUserAgentString(web.getSettings().getUserAgentString() + " FK115Android/1");
        web.setWebViewClient(new WebViewClient() {
            @Override public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                Uri uri = request.getUrl();
                if (!request.isForMainFrame()) return !inside(uri);
                if (inside(uri)) {
                    if ("/__android/pick-folder".equals(uri.getPath()) && request.hasGesture()) {
                        pickFolder();
                        return true;
                    }
                    if ("/__android/stop".equals(uri.getPath()) && request.hasGesture()) {
                        new AlertDialog.Builder(MainActivity.this).setMessage("停止本机书库和后台任务？")
                            .setNegativeButton("取消", null)
                            .setPositiveButton("停止", (dialog, action) -> {
                                startService(new Intent(MainActivity.this, ReaderService.class).setAction(ReaderService.STOP));
                                finish();
                            }).show();
                        return true;
                    }
                    return false;
                }
                if (request.hasGesture() && ("https".equals(uri.getScheme()) || "http".equals(uri.getScheme()))) {
                    try { startActivity(new Intent(Intent.ACTION_VIEW, uri)); } catch (ActivityNotFoundException ignored) {}
                }
                return true;
            }
            @Override public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest request) {
                if (inside(request.getUrl())) return null;
                return new WebResourceResponse("text/plain", "UTF-8", null);
            }
            @Override public void onPageFinished(WebView view, String url) {
                if (inside(Uri.parse(url))) showPickedFolder();
            }
        });
        show(web);
        CookieManager cookies = CookieManager.getInstance();
        cookies.setAcceptCookie(true);
        cookies.setAcceptThirdPartyCookies(web, false);
        cookies.setCookie(origin, "fk115_mobile_guard=" + instance.token() + "; Path=/; HttpOnly; SameSite=Strict",
            ok -> { if (ok && web != null && origin.equals(readyOrigin)) web.loadUrl(readyOrigin + "/"); });
    }

    private void show(View content) {
        frame.removeAllViews();
        frame.addView(content, new FrameLayout.LayoutParams(-1, -1));
    }

    private boolean inside(Uri uri) {
        return origin.equals("http://" + uri.getEncodedAuthority()) && "http".equals(uri.getScheme());
    }

    private void pickFolder() {
        Intent intent = new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION);
        try { startActivityForResult(intent, FOLDER); }
        catch (ActivityNotFoundException e) { Toast.makeText(this, "系统目录选择器不可用", Toast.LENGTH_LONG).show(); }
    }

    @Override protected void onActivityResult(int request, int result, Intent data) {
        super.onActivityResult(request, result, data);
        if (request != FOLDER || result != RESULT_OK || data == null || data.getData() == null) return;
        try {
            Uri tree = data.getData();
            getContentResolver().takePersistableUriPermission(tree, Intent.FLAG_GRANT_READ_URI_PERMISSION);
            pendingFolder = tree.toString();
            showPickedFolder();
        } catch (SecurityException e) {
            Toast.makeText(this, "未能保存目录读取权限", Toast.LENGTH_LONG).show();
        }
    }

    private void showPickedFolder() {
        if (pendingFolder == null || web == null || web.getUrl() == null || !inside(Uri.parse(web.getUrl()))) return;
        String script = "window.dispatchEvent(new CustomEvent('fk115-folder',{detail:" + JSONObject.quote(pendingFolder) + "}))";
        pendingFolder = null;
        web.evaluateJavascript(script, null);
    }

    @Override public void onBackPressed() {
        if (web != null && web.canGoBack()) web.goBack();
        else super.onBackPressed();
    }

    @Override protected void onDestroy() {
        if (service != null) service.unwatch(observer);
        if (bound) unbindService(connection);
        if (web != null) web.destroy();
        super.onDestroy();
    }
}
