package io.github.xjogger.fakekomga115;

import android.app.Activity;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageInstaller;
import android.net.Uri;
import android.os.Build;
import android.provider.Settings;

import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;

final class UpdateInstaller {
    private UpdateInstaller() {}

    static boolean canInstallPackages(Context context) {
        if (context == null) return false;
        if (Build.VERSION.SDK_INT >= 26) {
            try {
                return context.getPackageManager().canRequestPackageInstalls();
            } catch (Throwable ignored) {
                return false;
            }
        }
        return true;
    }

    static void openInstallPermissionSettings(Activity activity) {
        if (activity == null) return;
        Intent intent = new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                Uri.parse("package:" + activity.getPackageName()));
        activity.startActivity(intent);
    }

    static void installApk(Context context, File apkFile) throws IOException {
        if (context == null || apkFile == null || !apkFile.isFile() || apkFile.length() <= 0) {
            throw new IOException("APK file is missing or empty");
        }
        PackageInstaller installer = context.getPackageManager().getPackageInstaller();
        PackageInstaller.SessionParams params =
                new PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL);
        params.setSize(apkFile.length());
        int sessionId = installer.createSession(params);
        PackageInstaller.Session session = null;
        try {
            session = installer.openSession(sessionId);
            try (InputStream in = new FileInputStream(apkFile);
                 OutputStream out = session.openWrite("mangaalpha-update.apk", 0, apkFile.length())) {
                byte[] buf = new byte[64 * 1024];
                int read;
                while ((read = in.read(buf)) != -1) {
                    out.write(buf, 0, read);
                }
                session.fsync(out);
            }
            Intent callbackIntent = new Intent(context, InstallStatusReceiver.class);
            int flags = PendingIntent.FLAG_UPDATE_CURRENT;
            if (Build.VERSION.SDK_INT >= 31) {
                // Explicit non-exported BroadcastReceiver target; FLAG_MUTABLE is required so Android
                // populates EXTRA_STATUS and EXTRA_INTENT for PackageInstaller.Session.commit().
                flags |= PendingIntent.FLAG_MUTABLE;
            }
            PendingIntent pending = PendingIntent.getBroadcast(context, sessionId, callbackIntent, flags);
            session.commit(pending.getIntentSender());
        } catch (IOException | RuntimeException e) {
            if (session != null) {
                try { session.abandon(); } catch (Throwable ignored) {}
            }
            throw e;
        } finally {
            if (session != null) {
                try { session.close(); } catch (Throwable ignored) {}
            }
        }
    }
}
