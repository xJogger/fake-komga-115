package io.github.xjogger.fakekomga115;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.net.Uri;
import android.os.Binder;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;

import java.io.File;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import mobile.Instance;
import mobile.Mobile;

public final class ReaderService extends Service {
    static final String STOP = "io.github.xjogger.fakekomga115.STOP";
    private static final ExecutorService ENGINE = Executors.newSingleThreadExecutor();
    private final Handler main = new Handler(Looper.getMainLooper());
    private final LocalBinder binder = new LocalBinder();
    private volatile Instance server;
    private volatile boolean stopping;
    private boolean starting;
    private String message = "正在启动本机书库…";
    private Observer observer;

    interface Observer { void changed(Instance instance, String message); }

    final class LocalBinder extends Binder {
        void watch(Observer value) {
            observer = value;
            publish();
        }
        void unwatch(Observer value) {
            if (observer == value) observer = null;
        }
    }

    @Override public IBinder onBind(Intent intent) { return binder; }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && STOP.equals(intent.getAction())) {
            stopEngine("本机书库已停止");
            return START_NOT_STICKY;
        }
        if (stopping || starting || server != null) return START_NOT_STICKY;
        starting = true;
        startForeground(1, notification());
        ENGINE.execute(() -> {
            if (stopping) return;
            try {
                int port = getSharedPreferences("embedded", MODE_PRIVATE).getInt("port", 0);
                String dataDir = new File(getFilesDir(), "library").getAbsolutePath();
                Documents documents = new Documents(getContentResolver());
                try {
                    server = Mobile.start(dataDir, documents, port);
                } catch (Exception e) {
                    if (port == 0 || stopping) throw e;
                    server = Mobile.start(dataDir, documents, 0);
                }
                getSharedPreferences("embedded", MODE_PRIVATE).edit()
                    .putInt("port", Uri.parse(server.origin()).getPort()).apply();
                main.post(() -> { if (!stopping) publish(); });
            } catch (Exception | LinkageError e) {
                main.post(() -> { if (!stopping) stopEngine("本机书库启动失败，请重新打开应用重试"); });
            }
        });
        return START_NOT_STICKY;
    }

    private Notification notification() {
        final String channel = "embedded-reader";
        NotificationManager nm = getSystemService(NotificationManager.class);
        nm.createNotificationChannel(new NotificationChannel(channel, "本机书库", NotificationManager.IMPORTANCE_LOW));
        PendingIntent open = PendingIntent.getActivity(this, 0, new Intent(this, MainActivity.class),
            PendingIntent.FLAG_IMMUTABLE);
        PendingIntent stop = PendingIntent.getService(this, 1, new Intent(this, ReaderService.class).setAction(STOP),
            PendingIntent.FLAG_IMMUTABLE);
        return new Notification.Builder(this, channel).setSmallIcon(android.R.drawable.ic_menu_agenda)
            .setContentTitle("MangaAlpha 正在运行").setContentText("仅在本机提供书架与阅读器")
            .setContentIntent(open).addAction(new Notification.Action.Builder(
                    android.R.drawable.ic_menu_close_clear_cancel, "停止", stop).build()).setOngoing(true).build();
    }

    private void publish() {
        if (observer != null) observer.changed(stopping ? null : server, message);
    }

    private void stopEngine(String reason) {
        if (stopping) return;
        stopping = true;
        message = reason;
        publish();
        ENGINE.execute(() -> {
            if (server != null) server.stop();
            server = null;
        });
        stopForeground(STOP_FOREGROUND_REMOVE);
        stopSelf();
    }

    @Override public void onDestroy() {
        observer = null;
        stopEngine("本机书库已停止");
        super.onDestroy();
    }
}
