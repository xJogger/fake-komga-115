package io.github.xjogger.fakekomga115;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.content.SharedPreferences;
import android.net.Uri;
import android.os.Binder;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;

import java.io.File;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import mobile.Instance;
import mobile.Mobile;

public final class ReaderService extends Service {
    static final String STOP = "io.github.xjogger.fakekomga115.STOP";
    static final String PREFS = "embedded";
    static final String KEY_PORT = "port";
    static final String KEY_LAN_ENABLED = "lan_enabled";
    static final String KEY_LAN_PORT = "lan_port";
    static final int DEFAULT_LAN_PORT = 25600;

    private static final String CHANNEL = "embedded-reader";
    private static final ExecutorService ENGINE = Executors.newSingleThreadExecutor();
    private final Handler main = new Handler(Looper.getMainLooper());
    private final LocalBinder binder = new LocalBinder();
    private final CopyOnWriteArrayList<Observer> observers = new CopyOnWriteArrayList<>();

    private volatile Instance server;
    private volatile boolean stopping;
    private volatile boolean starting;
    private volatile String message = "正在启动本机书库…";
    private volatile String lanError = "";

    interface Observer { void changed(Instance instance, String message); }

    final class LocalBinder extends Binder {
        void watch(Observer value) {
            if (value != null && !observers.contains(value)) {
                observers.add(value);
            }
            if (value != null) {
                value.changed(stopping ? null : server, message);
            }
        }

        void unwatch(Observer value) {
            observers.remove(value);
        }

        Instance getInstance() { return stopping ? null : server; }
        String getMessage() { return message; }
        boolean isRunning() { return !stopping && server != null; }
        boolean isStarting() { return starting; }

        boolean isLanEnabledPref() {
            return prefs().getBoolean(KEY_LAN_ENABLED, false);
        }

        int getLanPortPref() {
            int p = prefs().getInt(KEY_LAN_PORT, DEFAULT_LAN_PORT);
            return (p >= 1024 && p <= 65535) ? p : DEFAULT_LAN_PORT;
        }

        boolean isLanActive() {
            Instance s = server;
            return !stopping && s != null && s.lanActive();
        }

        int getLanActivePort() {
            Instance s = server;
            return (!stopping && s != null) ? s.lanPort() : 0;
        }

        String getLanError() {
            Instance s = server;
            if (!stopping && s != null) {
                String err = s.lanError();
                if (err != null && !err.isEmpty()) return err;
            }
            return lanError;
        }

        void applyLanConfig(boolean enabled, int port, Runnable onComplete) {
            int validPort = (port >= 1024 && port <= 65535) ? port : DEFAULT_LAN_PORT;
            prefs().edit().putBoolean(KEY_LAN_ENABLED, enabled).putInt(KEY_LAN_PORT, validPort).apply();
            ENGINE.execute(() -> {
                Instance s = server;
                if (s == null || stopping) {
                    lanError = enabled ? "本机书库尚未启动" : "";
                } else if (enabled) {
                    try {
                        s.enableLan(validPort);
                        lanError = "";
                        DiagnosticsLog.append(ReaderService.this, "info", "android_lan", "enable", "OK",
                                "LAN server enabled on port " + validPort);
                    } catch (Exception e) {
                        lanError = e.getMessage() != null ? e.getMessage() : "启动局域网监听失败";
                        DiagnosticsLog.append(ReaderService.this, "error", "android_lan", "enable", "BIND_OR_AUTH",
                                lanError);
                    }
                } else {
                    try {
                        s.disableLan();
                    } catch (Exception ignored) {}
                    lanError = "";
                    DiagnosticsLog.append(ReaderService.this, "info", "android_lan", "disable", "OK",
                            "LAN server disabled");
                }
                main.post(() -> {
                    refreshNotification();
                    publish();
                    if (onComplete != null) onComplete.run();
                });
            });
        }

        void setDiagnosticsVerbose(boolean verbose, Runnable onComplete) {
            ENGINE.execute(() -> {
                Instance s = server;
                if (s != null && !stopping) {
                    try { s.setDiagnosticsVerbose(verbose); } catch (Exception ignored) {}
                }
                main.post(() -> {
                    if (onComplete != null) onComplete.run();
                });
            });
        }

        void startIfStopped() {
            if (server == null && !starting) {
                stopping = false;
                startEngine();
            }
        }

        void stopNow() {
            stopEngine("本机书库已停止");
        }
    }

    private SharedPreferences prefs() {
        return getSharedPreferences(PREFS, MODE_PRIVATE);
    }

    @Override public IBinder onBind(Intent intent) { return binder; }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && STOP.equals(intent.getAction())) {
            stopEngine("本机书库已停止");
            return START_NOT_STICKY;
        }
        if (stopping || starting || server != null) return START_NOT_STICKY;
        startEngine();
        return START_NOT_STICKY;
    }

    private void startEngine() {
        starting = true;
        message = "正在启动本机书库…";
        publish();
        startForeground(1, buildNotification());
        DiagnosticsLog.append(this, "info", "android_service", "start", "STARTING", "foreground service started");
        ENGINE.execute(() -> {
            if (stopping) {
                starting = false;
                return;
            }
            try {
                int port = prefs().getInt(KEY_PORT, 0);
                String dataDir = new File(getFilesDir(), "library").getAbsolutePath();
                String cacheDir = getCacheDir().getAbsolutePath();
                Documents documents = new Documents(this, getContentResolver());
                try {
                    server = Mobile.start(dataDir, cacheDir, documents, port);
                } catch (Exception e) {
                    if (port == 0 || stopping) throw e;
                    server = Mobile.start(dataDir, cacheDir, documents, 0);
                }
                prefs().edit().putInt(KEY_PORT, Uri.parse(server.origin()).getPort()).apply();
                DiagnosticsLog.append(this, "info", "android_service", "ready", "OK", "embedded loopback server ready");

                // Restore remembered LAN server setting without ever blocking loopback startup.
                if (prefs().getBoolean(KEY_LAN_ENABLED, false)) {
                    int lanPort = prefs().getInt(KEY_LAN_PORT, DEFAULT_LAN_PORT);
                    if (lanPort < 1024 || lanPort > 65535) lanPort = DEFAULT_LAN_PORT;
                    try {
                        server.enableLan(lanPort);
                        lanError = "";
                        DiagnosticsLog.append(this, "info", "android_lan", "auto_restore", "OK",
                                "restored LAN listener on port " + lanPort);
                    } catch (Exception lanEx) {
                        lanError = lanEx.getMessage() != null ? lanEx.getMessage() : "恢复局域网监听失败";
                        DiagnosticsLog.append(this, "warn", "android_lan", "auto_restore", "LAN_START_FAILED",
                                lanError);
                    }
                } else {
                    lanError = "";
                }

                starting = false;
                main.post(() -> {
                    if (!stopping) {
                        refreshNotification();
                        publish();
                    }
                });
            } catch (Exception | LinkageError e) {
                starting = false;
                String detail = e.getClass().getSimpleName() + ": " + (e.getMessage() != null ? e.getMessage() : "startup failed");
                DiagnosticsLog.append(this, "error", "android_service", "start_failed", "STARTUP_ERROR", detail);
                main.post(() -> { if (!stopping) stopEngine("本机书库启动失败，请打开「手机端设置」导出诊断日志或重试"); });
            }
        });
    }

    private Notification buildNotification() {
        NotificationManager nm = getSystemService(NotificationManager.class);
        if (nm != null) {
            nm.createNotificationChannel(new NotificationChannel(CHANNEL, "本机书库", NotificationManager.IMPORTANCE_LOW));
        }
        Intent openIntent = new Intent(this, MainActivity.class)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_SINGLE_TOP);
        Intent settingsIntent = new Intent(this, SettingsActivity.class)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_SINGLE_TOP);
        PendingIntent open = PendingIntent.getActivity(this, 0, openIntent,
            PendingIntent.FLAG_IMMUTABLE);
        PendingIntent settings = PendingIntent.getActivity(this, 2, settingsIntent,
            PendingIntent.FLAG_IMMUTABLE);
        PendingIntent stop = PendingIntent.getService(this, 1, new Intent(this, ReaderService.class).setAction(STOP),
            PendingIntent.FLAG_IMMUTABLE);

        String contentText = "仅本机运行 · 下拉或长按展开设置/停止";
        Instance s = server;
        if (!stopping && s != null && s.lanActive()) {
            contentText = "局域网已开 (端口 " + s.lanPort() + ") · 下拉或长按展开按钮";
        }
        return new Notification.Builder(this, CHANNEL)
            .setSmallIcon(android.R.drawable.ic_menu_agenda)
            .setContentTitle("MangaAlpha 正在运行")
            .setContentText(contentText)
            .setContentIntent(open)
            .addAction(new Notification.Action.Builder(
                    android.R.drawable.ic_menu_preferences, "手机端设置", settings).build())
            .addAction(new Notification.Action.Builder(
                    android.R.drawable.ic_menu_close_clear_cancel, "停止", stop).build())
            .setOngoing(true)
            .build();
    }

    private void refreshNotification() {
        if (stopping) return;
        NotificationManager nm = getSystemService(NotificationManager.class);
        if (nm != null) {
            try { nm.notify(1, buildNotification()); } catch (Exception ignored) {}
        }
    }

    private void publish() {
        Instance current = stopping ? null : server;
        for (Observer o : observers) {
            o.changed(current, message);
        }
    }

    private void stopEngine(String reason) {
        if (stopping) return;
        stopping = true;
        starting = false;
        message = reason;
        DiagnosticsLog.append(this, "info", "android_service", "stop", "STOPPED", reason);
        publish();
        ENGINE.execute(() -> {
            if (server != null) server.stop();
            server = null;
        });
        stopForeground(STOP_FOREGROUND_REMOVE);
        stopSelf();
    }

    @Override public void onDestroy() {
        observers.clear();
        stopEngine("本机书库已停止");
        super.onDestroy();
    }
}
