package io.github.xjogger.fakekomga115;

import android.app.Activity;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.ComponentName;
import android.content.Context;
import android.content.Intent;
import android.content.ServiceConnection;
import android.content.SharedPreferences;
import android.content.res.Configuration;
import android.graphics.Insets;
import android.graphics.Typeface;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.IBinder;
import android.text.InputType;
import android.view.View;
import android.view.Window;
import android.view.WindowInsets;
import android.view.WindowInsetsController;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.Switch;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONObject;

import java.io.File;
import java.io.OutputStream;
import java.net.Inet4Address;
import java.net.InetAddress;
import java.net.NetworkInterface;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Date;
import java.util.Enumeration;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicInteger;

import mobile.Instance;

public final class SettingsActivity extends Activity {
    private static final int EXPORT_DOCUMENT = 21;
    private static final int MAX_SHARE_CHARS = 100_000;
    private static final int COLOR_LIGHT_BG = 0xFFF4F6FA;
    private static final int COLOR_DARK_BG = 0xFF0F1117;

    private ScrollView rootScroll;
    private TextView serviceStatusView;
    private Switch lanSwitch;
    private EditText lanPortInput;
    private TextView lanStatusView;
    private TextView lanAddressesView;
    private Button copyLanBtn;
    private Switch verboseSwitch;
    private TextView diagPreviewView;
    private TextView updateStatusView;
    private TextView updateNotesView;
    private ProgressBar updateProgressBar;
    private TextView updateProgressText;
    private Button checkUpdateBtn;
    private Button installUpdateBtn;

    private final ExecutorService diagExecutor = Executors.newSingleThreadExecutor();
    private final AtomicInteger diagSeq = new AtomicInteger();

    private ReaderService.LocalBinder service;
    private boolean bound;
    private boolean updatingUi;
    private String pendingExportText;
    private String primaryLanUrl = "";
    private boolean updateBusy;
    private boolean updateCheckedOnConnect;
    private boolean updateHasNew;
    private String updateLatestVersion = "";
    private String updateAssetUrl = "";
    private File pendingInstallApk;
    private boolean pendingDownloadAfterPerm;

    private final ReaderService.Observer observer = (instance, message) -> runOnUiThread(this::refreshUi);
    private final ServiceConnection connection = new ServiceConnection() {
        @Override public void onServiceConnected(ComponentName name, IBinder binder) {
            service = (ReaderService.LocalBinder) binder;
            service.watch(observer);
            if (!updateCheckedOnConnect) {
                updateCheckedOnConnect = true;
                requestUpdateStatus(false);
            }
        }
        @Override public void onServiceDisconnected(ComponentName name) {
            service = null;
            refreshUi();
        }
    };

    @Override protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        boolean dark = (getResources().getConfiguration().uiMode & Configuration.UI_MODE_NIGHT_MASK)
                == Configuration.UI_MODE_NIGHT_YES;
        int bgColor = dark ? COLOR_DARK_BG : COLOR_LIGHT_BG;
        int cardColor = dark ? 0xFF1C212C : 0xFFFFFFFF;
        int textColor = dark ? 0xFFEEF1F7 : 0xFF141923;
        int mutedColor = dark ? 0xFF9AA4B8 : 0xFF4E5A6E;

        rootScroll = new ScrollView(this);
        rootScroll.setBackgroundColor(bgColor);
        if (Build.VERSION.SDK_INT >= 30) {
            getWindow().setDecorFitsSystemWindows(false);
            rootScroll.setOnApplyWindowInsetsListener((v, insets) -> {
                Insets bars = insets.getInsets(WindowInsets.Type.systemBars()
                        | WindowInsets.Type.displayCutout() | WindowInsets.Type.ime());
                v.setPadding(bars.left, bars.top, bars.right, bars.bottom);
                return WindowInsets.CONSUMED;
            });
        }

        LinearLayout container = new LinearLayout(this);
        container.setOrientation(LinearLayout.VERTICAL);
        container.setPadding(dp(16), dp(16), dp(16), dp(28));
        rootScroll.addView(container);

        // Header
        TextView title = new TextView(this);
        title.setText("MangaAlpha · 手机端设置");
        title.setTextSize(20f);
        title.setTypeface(null, Typeface.BOLD);
        title.setTextColor(textColor);
        container.addView(title);

        TextView subtitle = new TextView(this);
        subtitle.setText("即使网页白屏或本机服务异常，仍可在此查看状态、控制局域网服务与导出脱敏诊断日志。提示：在小米等系统通知栏中，下拉或长按常驻通知可展开「手机端设置 / 停止」快捷按钮。");
        subtitle.setTextSize(12.5f);
        subtitle.setTextColor(mutedColor);
        subtitle.setPadding(0, dp(4), 0, dp(14));
        container.addView(subtitle);

        // Card 1: Service Status
        LinearLayout serviceCard = makeCard(cardColor);
        serviceCard.addView(makeSectionTitle("本机服务状态", textColor));
        serviceStatusView = new TextView(this);
        serviceStatusView.setTextSize(13.5f);
        serviceStatusView.setTextColor(mutedColor);
        serviceStatusView.setPadding(0, dp(6), 0, dp(10));
        serviceCard.addView(serviceStatusView);

        LinearLayout serviceActions = makeButtonRow();
        Button backBtn = new Button(this);
        backBtn.setText("返回书架");
        backBtn.setOnClickListener(v -> finish());
        Button startBtn = new Button(this);
        startBtn.setText("启动服务");
        startBtn.setOnClickListener(v -> {
            try {
                startForegroundService(new Intent(this, ReaderService.class));
                if (service != null) service.startIfStopped();
            } catch (Throwable t) {
                DiagnosticsLog.append(this, "error", "android_service", "manual_start",
                        t.getClass().getSimpleName(), t.getMessage());
                Toast.makeText(this, "启动服务失败，请查看诊断日志", Toast.LENGTH_LONG).show();
            }
            refreshUi();
        });
        Button stopBtn = new Button(this);
        stopBtn.setText("停止服务");
        stopBtn.setOnClickListener(v -> {
            try {
                if (service != null) service.stopNow();
                else startService(new Intent(this, ReaderService.class).setAction(ReaderService.STOP));
            } catch (Throwable t) {
                DiagnosticsLog.append(this, "error", "android_service", "manual_stop",
                        t.getClass().getSimpleName(), t.getMessage());
            }
            refreshUi();
        });
        serviceActions.addView(backBtn);
        serviceActions.addView(startBtn);
        serviceActions.addView(stopBtn);
        serviceCard.addView(serviceActions);
        container.addView(serviceCard);

        // Card 1.5: App Update (verified SHA256SUMS + PackageInstaller)
        LinearLayout updateCard = makeCard(cardColor);
        updateCard.addView(makeSectionTitle("应用版本与更新", textColor));
        updateStatusView = new TextView(this);
        updateStatusView.setTextSize(13f);
        updateStatusView.setTextColor(mutedColor);
        updateStatusView.setPadding(0, dp(6), 0, dp(6));
        updateStatusView.setText("正在读取版本信息…");
        updateCard.addView(updateStatusView);

        updateNotesView = new TextView(this);
        updateNotesView.setTextSize(12f);
        updateNotesView.setTextColor(mutedColor);
        updateNotesView.setBackgroundColor(dark ? 0xFF131720 : 0xFFF8F9FC);
        updateNotesView.setPadding(dp(10), dp(8), dp(10), dp(8));
        updateNotesView.setMaxLines(8);
        updateNotesView.setVisibility(View.GONE);
        LinearLayout.LayoutParams notesLp = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        notesLp.bottomMargin = dp(8);
        updateNotesView.setLayoutParams(notesLp);
        updateCard.addView(updateNotesView);

        updateProgressBar = new ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal);
        updateProgressBar.setMax(100);
        updateProgressBar.setVisibility(View.GONE);
        updateCard.addView(updateProgressBar);

        updateProgressText = new TextView(this);
        updateProgressText.setTextSize(12f);
        updateProgressText.setTextColor(mutedColor);
        updateProgressText.setPadding(0, dp(4), 0, dp(8));
        updateProgressText.setVisibility(View.GONE);
        updateCard.addView(updateProgressText);

        LinearLayout updateActions = makeButtonRow();
        checkUpdateBtn = new Button(this);
        checkUpdateBtn.setText("检查更新");
        checkUpdateBtn.setOnClickListener(v -> requestUpdateStatus(true));
        installUpdateBtn = new Button(this);
        installUpdateBtn.setText("下载并安装更新");
        installUpdateBtn.setVisibility(View.GONE);
        installUpdateBtn.setOnClickListener(v -> startDownloadAndInstallUpdate());
        updateActions.addView(checkUpdateBtn);
        updateActions.addView(installUpdateBtn);
        updateCard.addView(updateActions);
        container.addView(updateCard);

        // Card 2: LAN Server
        LinearLayout lanCard = makeCard(cardColor);
        lanCard.addView(makeSectionTitle("手机充当局域网服务器（默认关闭）", textColor));
        TextView lanDesc = new TextView(this);
        lanDesc.setText("开启后额外监听 0.0.0.0:<端口>，局域网浏览器及 Komga / Mihon / Koharia 客户端可按账号密码或 API Key 访问。本机 WebView 仍独立走回环地址保护；重启应用会记住此开关与端口。");
        lanDesc.setTextSize(12.5f);
        lanDesc.setTextColor(mutedColor);
        lanDesc.setPadding(0, dp(4), 0, dp(10));
        lanCard.addView(lanDesc);

        lanSwitch = new Switch(this);
        lanSwitch.setText("开启局域网访问");
        lanSwitch.setTextColor(textColor);
        lanSwitch.setOnCheckedChangeListener((buttonView, isChecked) -> {
            if (updatingUi) return;
            applyLanFromControls(isChecked);
        });
        lanCard.addView(lanSwitch);

        LinearLayout portRow = new LinearLayout(this);
        portRow.setOrientation(LinearLayout.HORIZONTAL);
        portRow.setPadding(0, dp(8), 0, dp(6));
        TextView portLabel = new TextView(this);
        portLabel.setText("监听端口：");
        portLabel.setTextColor(textColor);
        lanPortInput = new EditText(this);
        lanPortInput.setInputType(InputType.TYPE_CLASS_NUMBER);
        lanPortInput.setTextColor(textColor);
        lanPortInput.setMinWidth(dp(100));
        Button applyPortBtn = new Button(this);
        applyPortBtn.setText("应用端口");
        applyPortBtn.setOnClickListener(v -> applyLanFromControls(lanSwitch.isChecked()));
        portRow.addView(portLabel);
        portRow.addView(lanPortInput);
        portRow.addView(applyPortBtn);
        lanCard.addView(portRow);

        lanStatusView = new TextView(this);
        lanStatusView.setTextSize(12.5f);
        lanStatusView.setTextColor(mutedColor);
        lanStatusView.setPadding(0, dp(4), 0, dp(4));
        lanCard.addView(lanStatusView);

        lanAddressesView = new TextView(this);
        lanAddressesView.setTextSize(13f);
        lanAddressesView.setTypeface(Typeface.MONOSPACE);
        lanAddressesView.setTextColor(textColor);
        lanAddressesView.setPadding(0, dp(4), 0, dp(8));
        lanCard.addView(lanAddressesView);

        copyLanBtn = new Button(this);
        copyLanBtn.setText("复制局域网地址");
        copyLanBtn.setOnClickListener(v -> {
            if (primaryLanUrl.isEmpty()) return;
            ClipboardManager cm = (ClipboardManager) getSystemService(Context.CLIPBOARD_SERVICE);
            if (cm != null) {
                cm.setPrimaryClip(ClipData.newPlainText("MangaAlpha LAN URL", primaryLanUrl));
                Toast.makeText(this, "已复制：" + primaryLanUrl, Toast.LENGTH_SHORT).show();
            }
        });
        lanCard.addView(copyLanBtn);
        container.addView(lanCard);

        // Card 3: Sanitized Diagnostics
        LinearLayout diagCard = makeCard(cardColor);
        diagCard.addView(makeSectionTitle("脱敏诊断日志与导出", textColor));
        TextView diagDesc = new TextView(this);
        diagDesc.setText("默认精简记录关键阶段与错误，排障时可临时开启详细模式。仅记录脱敏后的版本、阶段、耗时与错误类别，绝不包含密码、Token、Cookie、完整 URL 或私人路径。");
        diagDesc.setTextSize(12.5f);
        diagDesc.setTextColor(mutedColor);
        diagDesc.setPadding(0, dp(4), 0, dp(10));
        diagCard.addView(diagDesc);

        verboseSwitch = new Switch(this);
        verboseSwitch.setText("详细排障模式");
        verboseSwitch.setTextColor(textColor);
        verboseSwitch.setOnCheckedChangeListener((buttonView, isChecked) -> {
            if (updatingUi) return;
            if (service != null) {
                service.setDiagnosticsVerbose(isChecked, this::refreshDiagnosticsPreview);
            } else {
                Toast.makeText(this, "服务未运行，启动后可切换详细模式", Toast.LENGTH_SHORT).show();
                updatingUi = true;
                verboseSwitch.setChecked(false);
                updatingUi = false;
            }
        });
        diagCard.addView(verboseSwitch);

        LinearLayout diagActionsRow1 = makeButtonRow();
        LinearLayout diagActionsRow2 = makeButtonRow();
        Button refreshDiagBtn = new Button(this);
        refreshDiagBtn.setText("刷新预览");
        refreshDiagBtn.setOnClickListener(v -> refreshDiagnosticsPreview());
        Button saveDiagBtn = new Button(this);
        saveDiagBtn.setText("保存文件");
        saveDiagBtn.setOnClickListener(v -> startSaveDiagnosticsDocument());
        Button shareDiagBtn = new Button(this);
        shareDiagBtn.setText("分享日志");
        shareDiagBtn.setOnClickListener(v -> shareDiagnosticsText());
        Button clearDiagBtn = new Button(this);
        clearDiagBtn.setText("清空日志");
        clearDiagBtn.setOnClickListener(v -> {
            DiagnosticsLog.clearAll(this, service != null ? service.getInstance() : null);
            Toast.makeText(this, "诊断日志已清空", Toast.LENGTH_SHORT).show();
            refreshDiagnosticsPreview();
        });
        diagActionsRow1.addView(refreshDiagBtn);
        diagActionsRow1.addView(clearDiagBtn);
        diagActionsRow2.addView(saveDiagBtn);
        diagActionsRow2.addView(shareDiagBtn);
        diagCard.addView(diagActionsRow1);
        diagCard.addView(diagActionsRow2);

        diagPreviewView = new TextView(this);
        diagPreviewView.setTypeface(Typeface.MONOSPACE);
        diagPreviewView.setTextSize(11f);
        diagPreviewView.setTextColor(mutedColor);
        diagPreviewView.setBackgroundColor(dark ? 0xFF131720 : 0xFFF8F9FC);
        diagPreviewView.setPadding(dp(10), dp(10), dp(10), dp(10));
        diagPreviewView.setTextIsSelectable(true);
        LinearLayout.LayoutParams prevLp = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        prevLp.topMargin = dp(10);
        diagPreviewView.setLayoutParams(prevLp);
        diagCard.addView(diagPreviewView);
        container.addView(diagCard);

        setContentView(rootScroll);
        applySystemBars(bgColor, !dark);
        bound = bindService(new Intent(this, ReaderService.class), connection, Context.BIND_AUTO_CREATE);
        refreshUi();
    }

    @SuppressWarnings("deprecation")
    private void applySystemBars(int bgColor, boolean lightIcons) {
        try {
            Window window = getWindow();
            if (window == null) return;
            View decor = window.getDecorView();
            if (decor == null) return;
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
                int flags = decor.getSystemUiVisibility();
                if (lightIcons) flags |= View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
                else flags &= ~View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
                decor.setSystemUiVisibility(flags);
            }
        } catch (Throwable t) {
            DiagnosticsLog.append(this, "warn", "android_ui", "system_bars",
                    t.getClass().getSimpleName(), t.getMessage());
        }
    }

    private void applyLanFromControls(boolean enabled) {
        int port = ReaderService.DEFAULT_LAN_PORT;
        try {
            port = Integer.parseInt(lanPortInput.getText().toString().trim());
        } catch (NumberFormatException ignored) {}
        if (port < 1024 || port > 65535) {
            Toast.makeText(this, "端口号须在 1024 到 65535 之间", Toast.LENGTH_SHORT).show();
            refreshUi();
            return;
        }
        if (service != null) {
            service.applyLanConfig(enabled, port, this::refreshUi);
        } else {
            getSharedPreferences(ReaderService.PREFS, MODE_PRIVATE).edit()
                    .putBoolean(ReaderService.KEY_LAN_ENABLED, enabled)
                    .putInt(ReaderService.KEY_LAN_PORT, port)
                    .apply();
            Toast.makeText(this, "已保存设置，将在服务启动后生效", Toast.LENGTH_SHORT).show();
            refreshUi();
        }
    }

    private void refreshUi() {
        if (isFinishing() || isDestroyed()) return;
        updatingUi = true;
        try {
            SharedPreferences prefs = getSharedPreferences(ReaderService.PREFS, MODE_PRIVATE);
            boolean lanPref = service != null ? service.isLanEnabledPref() : prefs.getBoolean(ReaderService.KEY_LAN_ENABLED, false);
            int lanPort = service != null ? service.getLanPortPref() : prefs.getInt(ReaderService.KEY_LAN_PORT, ReaderService.DEFAULT_LAN_PORT);

            if (service != null && service.isRunning()) {
                Instance inst = service.getInstance();
                serviceStatusView.setText("运行中（本机回环：" + (inst != null ? inst.origin() : "-") + "）");
            } else if (service != null && service.isStarting()) {
                serviceStatusView.setText("正在启动本机书库…");
            } else {
                serviceStatusView.setText(service != null ? service.getMessage() : "未连接到后台服务");
            }

            lanSwitch.setChecked(lanPref);
            if (!lanPortInput.hasFocus()) {
                lanPortInput.setText(String.valueOf(lanPort));
            }

            boolean lanActive = service != null && service.isLanActive();
            int activePort = service != null ? service.getLanActivePort() : 0;
            String lanErr = service != null ? service.getLanError() : "";

            if (lanActive) {
                lanStatusView.setText("状态：已开启局域网监听（0.0.0.0:" + activePort + "）");
                List<String> urls = listLanUrls(activePort);
                primaryLanUrl = urls.isEmpty() ? "" : urls.get(0);
                if (urls.isEmpty()) {
                    lanAddressesView.setText("未检测到活跃的局域网 Wi-Fi/以太网 IPv4 地址");
                    copyLanBtn.setVisibility(View.GONE);
                } else {
                    StringBuilder sb = new StringBuilder();
                    for (String u : urls) {
                        if (sb.length() > 0) sb.append('\n');
                        sb.append(u);
                    }
                    lanAddressesView.setText(sb.toString());
                    copyLanBtn.setVisibility(View.VISIBLE);
                }
            } else {
                primaryLanUrl = "";
                copyLanBtn.setVisibility(View.GONE);
                lanAddressesView.setText("");
                if (lanErr != null && !lanErr.isEmpty()) {
                    lanStatusView.setText("状态：未开启（原因：" + lanErr + "）");
                } else if (lanPref) {
                    lanStatusView.setText("状态：已配置开启，等待本机服务就绪");
                } else {
                    lanStatusView.setText("状态：已关闭（仅本机回环可访问）");
                }
            }

            Instance inst = service != null ? service.getInstance() : null;
            verboseSwitch.setChecked(inst != null && inst.diagnosticsVerbose());
        } catch (Throwable t) {
            DiagnosticsLog.append(this, "error", "android_ui", "refresh_settings",
                    t.getClass().getSimpleName(), t.getMessage());
        } finally {
            updatingUi = false;
        }
        refreshDiagnosticsPreview();
    }

    private void refreshDiagnosticsPreview() {
        if (isFinishing() || isDestroyed() || diagExecutor.isShutdown()) return;
        final int seq = diagSeq.incrementAndGet();
        final Instance inst = service != null ? service.getInstance() : null;
        final Context appCtx = getApplicationContext();
        try {
            diagExecutor.execute(() -> {
                String text;
                try {
                    text = DiagnosticsLog.buildExportText(appCtx, inst);
                } catch (Throwable t) {
                    DiagnosticsLog.append(appCtx, "error", "android_diag", "preview",
                            t.getClass().getSimpleName(), t.getMessage());
                    text = "# 诊断日志加载失败\n" + t.getClass().getSimpleName();
                }
                final String shown = text.length() > 8000 ? text.substring(text.length() - 8000) : text;
                runOnUiThread(() -> {
                    if (seq != diagSeq.get() || isFinishing() || isDestroyed()) return;
                    diagPreviewView.setText(shown);
                });
            });
        } catch (RuntimeException ignored) {}
    }

    private void startSaveDiagnosticsDocument() {
        if (isFinishing() || isDestroyed() || diagExecutor.isShutdown()) return;
        final Instance inst = service != null ? service.getInstance() : null;
        final Context appCtx = getApplicationContext();
        try {
            diagExecutor.execute(() -> {
                final String text = DiagnosticsLog.buildExportText(appCtx, inst);
                runOnUiThread(() -> {
                    if (isFinishing() || isDestroyed()) return;
                    pendingExportText = text;
                    String ts = new SimpleDateFormat("yyyyMMdd-HHmmss", Locale.US).format(new Date());
                    Intent intent = new Intent(Intent.ACTION_CREATE_DOCUMENT);
                    intent.addCategory(Intent.CATEGORY_OPENABLE);
                    intent.setType("text/plain");
                    intent.putExtra(Intent.EXTRA_TITLE, "mangaalpha-android-diagnostics-" + ts + ".txt");
                    try {
                        startActivityForResult(intent, EXPORT_DOCUMENT);
                    } catch (Throwable e) {
                        shareDiagnosticsText();
                    }
                });
            });
        } catch (RuntimeException ignored) {}
    }

    private void shareDiagnosticsText() {
        if (isFinishing() || isDestroyed() || diagExecutor.isShutdown()) return;
        final Instance inst = service != null ? service.getInstance() : null;
        final Context appCtx = getApplicationContext();
        try {
            diagExecutor.execute(() -> {
                String text = DiagnosticsLog.buildExportText(appCtx, inst);
                if (text.length() > MAX_SHARE_CHARS) {
                    text = "# 提示：日志较长，分享仅包含末尾 " + MAX_SHARE_CHARS + " 字符，完整内容请使用「保存文件」。\n\n"
                            + text.substring(text.length() - MAX_SHARE_CHARS);
                }
                final String shareBody = text;
                runOnUiThread(() -> {
                    if (isFinishing() || isDestroyed()) return;
                    Intent send = new Intent(Intent.ACTION_SEND);
                    send.setType("text/plain");
                    send.putExtra(Intent.EXTRA_SUBJECT, "MangaAlpha Android Diagnostics");
                    send.putExtra(Intent.EXTRA_TEXT, shareBody);
                    try {
                        startActivity(Intent.createChooser(send, "分享脱敏诊断日志"));
                    } catch (Throwable e) {
                        DiagnosticsLog.append(this, "error", "android_diag", "share",
                                e.getClass().getSimpleName(), e.getMessage());
                        Toast.makeText(this, "无法打开分享面板，请改用「保存文件」", Toast.LENGTH_LONG).show();
                    }
                });
            });
        } catch (RuntimeException ignored) {}
    }

    @Override protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == EXPORT_DOCUMENT && resultCode == RESULT_OK && data != null && data.getData() != null && pendingExportText != null) {
            Uri uri = data.getData();
            try (OutputStream out = getContentResolver().openOutputStream(uri)) {
                if (out != null) {
                    out.write(pendingExportText.getBytes(StandardCharsets.UTF_8));
                    Toast.makeText(this, "诊断日志已保存", Toast.LENGTH_SHORT).show();
                }
            } catch (Exception e) {
                Toast.makeText(this, "保存失败：" + e.getMessage(), Toast.LENGTH_LONG).show();
            } finally {
                pendingExportText = null;
            }
        }
    }

    private static List<String> listLanUrls(int port) {
        List<String> out = new ArrayList<>();
        try {
            Enumeration<NetworkInterface> ifaces = NetworkInterface.getNetworkInterfaces();
            while (ifaces != null && ifaces.hasMoreElements()) {
                NetworkInterface nif = ifaces.nextElement();
                if (!nif.isUp() || nif.isLoopback() || nif.isVirtual()) continue;
                Enumeration<InetAddress> addrs = nif.getInetAddresses();
                while (addrs.hasMoreElements()) {
                    InetAddress addr = addrs.nextElement();
                    if (addr instanceof Inet4Address && !addr.isLoopbackAddress() && !addr.isLinkLocalAddress()) {
                        out.add("http://" + addr.getHostAddress() + ":" + port);
                    }
                }
            }
        } catch (Exception ignored) {}
        return out;
    }

    private LinearLayout makeCard(int bgColor) {
        LinearLayout card = new LinearLayout(this);
        card.setOrientation(LinearLayout.VERTICAL);
        card.setBackgroundColor(bgColor);
        card.setPadding(dp(14), dp(14), dp(14), dp(14));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        lp.bottomMargin = dp(14);
        card.setLayoutParams(lp);
        return card;
    }

    private TextView makeSectionTitle(String text, int color) {
        TextView tv = new TextView(this);
        tv.setText(text);
        tv.setTextSize(15.5f);
        tv.setTypeface(null, Typeface.BOLD);
        tv.setTextColor(color);
        return tv;
    }

    private LinearLayout makeButtonRow() {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        return row;
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    @Override protected void onResume() {
        super.onResume();
        if (pendingInstallApk != null && UpdateInstaller.canInstallPackages(this)) {
            File apk = pendingInstallApk;
            pendingInstallApk = null;
            launchApkInstaller(apk);
        } else if (pendingDownloadAfterPerm && UpdateInstaller.canInstallPackages(this)) {
            pendingDownloadAfterPerm = false;
            startDownloadAndInstallUpdate();
        }
    }

    private void requestUpdateStatus(boolean force) {
        if (updateBusy) return;
        if (service == null || !service.isRunning()) {
            String cur = service != null ? service.getAppVersionName() : "";
            updateStatusView.setText((cur.isEmpty() ? "" : "当前版本：" + cur + " · ") + "本机服务启动后可检查更新");
            return;
        }
        updateBusy = true;
        checkUpdateBtn.setEnabled(false);
        installUpdateBtn.setEnabled(false);
        if (force) {
            updateStatusView.setText("正在检查 GitHub 最新版本（自动复用 Bangumi 网络代理配置）…");
        }
        service.checkUpdate(force, (json, error) -> {
            if (isFinishing() || isDestroyed()) return;
            updateBusy = false;
            checkUpdateBtn.setEnabled(true);
            installUpdateBtn.setEnabled(true);
            if (error != null || json == null) {
                updateStatusView.setText("检查更新失败：" + (error != null ? error : "未知错误"));
                return;
            }
            applyUpdateStatusJson(json, force);
        });
    }

    private void applyUpdateStatusJson(String json, boolean manual) {
        try {
            JSONObject obj = new JSONObject(json);
            String cur = obj.optString("currentVersion", "");
            if (cur.isEmpty() && service != null) cur = service.getAppVersionName();
            String latest = obj.optString("latestVersion", "");
            boolean hasUpdate = obj.optBoolean("hasUpdate", false);
            String assetUrl = obj.optString("assetUrl", "");
            long assetSize = obj.optLong("assetSize", 0L);
            String notes = obj.optString("releaseNotes", "").trim();
            String lastErr = obj.optString("lastError", "").trim();
            String checkedAt = obj.optString("checkedAt", "").trim();

            updateHasNew = hasUpdate;
            updateLatestVersion = latest;
            updateAssetUrl = assetUrl;

            if (!lastErr.isEmpty() && manual) {
                updateStatusView.setText("当前版本：" + cur + "\n检查失败：" + lastErr);
            } else if (hasUpdate && !latest.isEmpty()) {
                String sizeHint = assetSize > 0 ? (" · 安装包约 " + formatMiB(assetSize)) : "";
                updateStatusView.setText("当前版本：" + cur + " → 发现新版本：" + latest + sizeHint + "\n下载时自动校验 SHA256SUMS 完整性。");
            } else if (!latest.isEmpty()) {
                updateStatusView.setText("当前版本：" + cur + "（已是最新版本 " + latest + "）"
                        + (checkedAt.isEmpty() ? "" : "\n最近检查：" + checkedAt.replace("T", " ").replace("Z", " UTC")));
            } else {
                updateStatusView.setText("当前版本：" + (cur.isEmpty() ? "-" : cur) + " · 尚未检查更新");
            }

            if (hasUpdate && !notes.isEmpty()) {
                updateNotesView.setText(notes.length() > 600 ? notes.substring(0, 600) + "…" : notes);
                updateNotesView.setVisibility(View.VISIBLE);
            } else {
                updateNotesView.setVisibility(View.GONE);
            }

            if (hasUpdate && !assetUrl.isEmpty()) {
                installUpdateBtn.setText("下载并安装 " + latest);
                installUpdateBtn.setVisibility(View.VISIBLE);
            } else {
                installUpdateBtn.setVisibility(View.GONE);
            }
        } catch (Throwable t) {
            updateStatusView.setText("解析更新状态失败：" + t.getMessage());
        }
    }

    private void startDownloadAndInstallUpdate() {
        if (updateBusy || !updateHasNew || updateAssetUrl.isEmpty()) return;
        if (service == null || !service.isRunning()) {
            Toast.makeText(this, "请先启动本机书库服务", Toast.LENGTH_SHORT).show();
            return;
        }
        if (!UpdateInstaller.canInstallPackages(this)) {
            pendingDownloadAfterPerm = true;
            Toast.makeText(this, "请先开启「允许安装未知应用」，返回后将自动继续下载更新", Toast.LENGTH_LONG).show();
            try {
                UpdateInstaller.openInstallPermissionSettings(this);
            } catch (Throwable t) {
                pendingDownloadAfterPerm = false;
                Toast.makeText(this, "无法打开安装权限设置：" + t.getMessage(), Toast.LENGTH_LONG).show();
            }
            return;
        }

        updateBusy = true;
        checkUpdateBtn.setEnabled(false);
        installUpdateBtn.setEnabled(false);
        updateProgressBar.setVisibility(View.VISIBLE);
        updateProgressBar.setIndeterminate(true);
        updateProgressText.setVisibility(View.VISIBLE);
        updateProgressText.setText("正在获取 SHA256SUMS 并准备下载 " + updateLatestVersion + "…");

        service.downloadVerifiedUpdate((downloaded, total) -> {
            if (isFinishing() || isDestroyed()) return;
            if (total > 0) {
                int pct = (int) Math.min(100, Math.max(0, (downloaded * 100L) / total));
                updateProgressBar.setIndeterminate(false);
                updateProgressBar.setProgress(pct);
                updateProgressText.setText("正在下载更新包：" + formatMiB(downloaded) + " / " + formatMiB(total) + " (" + pct + "%)");
            } else {
                updateProgressBar.setIndeterminate(true);
                updateProgressText.setText("正在下载更新包：已接收 " + formatMiB(downloaded));
            }
        }, (apkFile, error) -> {
            if (isFinishing() || isDestroyed()) return;
            updateBusy = false;
            checkUpdateBtn.setEnabled(true);
            installUpdateBtn.setEnabled(true);
            if (error != null || apkFile == null) {
                updateProgressBar.setVisibility(View.GONE);
                updateProgressText.setText("下载或 SHA256 校验失败：" + (error != null ? error : "未知错误"));
                Toast.makeText(this, "更新失败：" + (error != null ? error : "未知错误"), Toast.LENGTH_LONG).show();
                return;
            }
            updateProgressBar.setIndeterminate(false);
            updateProgressBar.setProgress(100);
            updateProgressText.setText("SHA256 校验通过，正在拉起系统安装界面…");
            launchApkInstaller(apkFile);
        });
    }

    private void launchApkInstaller(File apkFile) {
        if (!UpdateInstaller.canInstallPackages(this)) {
            pendingInstallApk = apkFile;
            Toast.makeText(this, "请授予安装未知应用权限后返回继续安装", Toast.LENGTH_LONG).show();
            try { UpdateInstaller.openInstallPermissionSettings(this); } catch (Throwable ignored) {}
            return;
        }
        try {
            UpdateInstaller.installApk(this, apkFile);
        } catch (Throwable t) {
            DiagnosticsLog.append(this, "error", "android_update", "commit_session",
                    t.getClass().getSimpleName(), t.getMessage());
            updateProgressText.setText("启动系统安装失败：" + t.getMessage());
            Toast.makeText(this, "启动安装失败：" + t.getMessage(), Toast.LENGTH_LONG).show();
        }
    }

    private static String formatMiB(long bytes) {
        if (bytes < 1024L * 1024L) {
            return String.format(Locale.US, "%.1f KiB", bytes / 1024.0);
        }
        return String.format(Locale.US, "%.2f MiB", bytes / (1024.0 * 1024.0));
    }

    @Override protected void onDestroy() {
        diagExecutor.shutdownNow();
        if (service != null) service.unwatch(observer);
        if (bound) unbindService(connection);
        super.onDestroy();
    }
}
