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
import android.content.res.Configuration;
import android.graphics.Color;
import android.graphics.Insets;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.IBinder;
import android.view.Gravity;
import android.view.View;
import android.view.Window;
import android.view.WindowInsets;
import android.view.WindowInsetsController;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.FrameLayout;
import android.widget.LinearLayout;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONObject;
import mobile.Instance;

public final class MainActivity extends Activity {
    private static final int FOLDER = 11;
    private static final int COLOR_LIGHT_BG = 0xFFF4F6FA;
    private static final int COLOR_DARK_BG = 0xFF0F1117;
    private static final int COLOR_READER_BG = 0xFF000000;

    private WebView web;
    private LinearLayout statusPane;
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

    private final class AndroidBridge {
        @JavascriptInterface
        public void setSystemBarTheme(String bgHex, boolean lightIcons) {
            runOnUiThread(() -> {
                if (web == null || web.getUrl() == null || !inside(Uri.parse(web.getUrl()))) return;
                int parsed = lightIcons ? COLOR_LIGHT_BG : COLOR_DARK_BG;
                if (bgHex != null && bgHex.matches("^#[0-9a-fA-F]{6}$")) {
                    try { parsed = Color.parseColor(bgHex); } catch (IllegalArgumentException ignored) {}
                }
                applySystemBarStyle(parsed, lightIcons);
            });
        }
    }

    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        boolean sysDark = isSystemNightMode();
        statusPane = new LinearLayout(this);
        statusPane.setOrientation(LinearLayout.VERTICAL);
        statusPane.setGravity(Gravity.CENTER_HORIZONTAL);
        statusPane.setPadding(64, 140, 64, 48);

        status = new TextView(this);
        status.setTextSize(16f);
        status.setTextColor(sysDark ? 0xFFEEF1F7 : 0xFF141923);
        status.setText("正在启动本机书库…");
        statusPane.addView(status);

        Button settingsBtn = new Button(this);
        settingsBtn.setText("手机端设置与诊断日志");
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        lp.topMargin = 36;
        settingsBtn.setLayoutParams(lp);
        settingsBtn.setOnClickListener(v -> startActivity(new Intent(this, SettingsActivity.class)));
        statusPane.addView(settingsBtn);

        frame = new FrameLayout(this);
        frame.addView(statusPane);
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
        applySystemBarStyle(sysDark ? COLOR_DARK_BG : COLOR_LIGHT_BG, !sysDark);

        Intent intent = new Intent(this, ReaderService.class);
        startForegroundService(intent);
        bound = bindService(intent, connection, Context.BIND_AUTO_CREATE);
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED && !getPreferences(0).getBoolean("notifications-asked", false)) {
            getPreferences(0).edit().putBoolean("notifications-asked", true).apply();
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 12);
        }
    }

    private boolean isSystemNightMode() {
        int mask = getResources().getConfiguration().uiMode & Configuration.UI_MODE_NIGHT_MASK;
        return mask == Configuration.UI_MODE_NIGHT_YES;
    }

    @SuppressWarnings("deprecation")
    private void applySystemBarStyle(int bgColor, boolean lightIcons) {
        if (frame != null) {
            frame.setBackgroundColor(bgColor);
        }
        Window window = getWindow();
        if (window == null) return;
        if (Build.VERSION.SDK_INT < 35) {
            window.setStatusBarColor(bgColor);
            window.setNavigationBarColor(bgColor);
        }
        if (Build.VERSION.SDK_INT >= 30) {
            WindowInsetsController ctrl = window.getInsetsController();
            if (ctrl != null) {
                int mask = WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS
                        | WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS;
                ctrl.setSystemBarsAppearance(lightIcons ? mask : 0, mask);
            }
        } else {
            View decor = window.getDecorView();
            int flags = decor.getSystemUiVisibility();
            if (lightIcons) {
                flags |= View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
                if (Build.VERSION.SDK_INT >= 27) {
                    flags |= View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
                }
            } else {
                flags &= ~View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
                if (Build.VERSION.SDK_INT >= 27) {
                    flags &= ~View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
                }
            }
            decor.setSystemUiVisibility(flags);
        }
    }

    private void syncSystemBarsFromPage(WebView view, String url) {
        if (view == null || url == null) return;
        Uri uri = Uri.parse(url);
        if (!inside(uri)) return;
        String path = uri.getPath();
        if (path != null && path.startsWith("/reader")) {
            applySystemBarStyle(COLOR_READER_BG, false);
            return;
        }
        view.evaluateJavascript(
            "(function(){return document.documentElement.getAttribute('data-theme')||'';})()",
            result -> {
                if (result == null) return;
                String clean = result.replace("\"", "").trim();
                if ("light".equals(clean)) {
                    applySystemBarStyle(COLOR_LIGHT_BG, true);
                } else if ("dark".equals(clean)) {
                    applySystemBarStyle(COLOR_DARK_BG, false);
                }
            }
        );
    }

    private void update(Instance instance, String message) {
        if (instance == null) {
            status.setText(message);
            if (web != null) {
                origin = "";
                show(statusPane);
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
        web.addJavascriptInterface(new AndroidBridge(), "FK115AndroidBridge");
        web.setWebViewClient(new WebViewClient() {
            @Override public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                Uri uri = request.getUrl();
                if (!request.isForMainFrame()) return !inside(uri);
                if (inside(uri)) {
                    if ("/__android/pick-folder".equals(uri.getPath()) && request.hasGesture()) {
                        pickFolder();
                        return true;
                    }
                    if ("/__android/settings".equals(uri.getPath()) && request.hasGesture()) {
                        startActivity(new Intent(MainActivity.this, SettingsActivity.class));
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
                if (url != null && inside(Uri.parse(url))) {
                    showPickedFolder();
                    syncSystemBarsFromPage(view, url);
                }
            }
            @Override public void onReceivedError(WebView view, WebResourceRequest request, WebResourceError error) {
                if (request != null && request.isForMainFrame()) {
                    DiagnosticsLog.append(MainActivity.this, "error", "android_webview", "main_frame_error",
                            "WEBVIEW_" + error.getErrorCode(), String.valueOf(error.getDescription()));
                }
            }
            @Override public void onReceivedHttpError(WebView view, WebResourceRequest request, WebResourceResponse errorResponse) {
                if (request != null && request.isForMainFrame() && errorResponse != null && errorResponse.getStatusCode() >= 500) {
                    DiagnosticsLog.append(MainActivity.this, "error", "android_webview", "http_error",
                            "HTTP_" + errorResponse.getStatusCode(), "main frame returned HTTP " + errorResponse.getStatusCode());
                }
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
            DiagnosticsLog.append(this, "error", "android_saf", "take_permission", "SECURITY_EXCEPTION", e.getMessage());
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
