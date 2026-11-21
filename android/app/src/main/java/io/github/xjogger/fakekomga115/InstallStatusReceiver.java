package io.github.xjogger.fakekomga115;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageInstaller;
import android.os.Build;
import android.widget.Toast;

public final class InstallStatusReceiver extends BroadcastReceiver {
    @Override
    @SuppressWarnings("deprecation")
    public void onReceive(Context context, Intent intent) {
        if (context == null || intent == null) return;
        int status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE);
        String msg = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE);

        if (status == PackageInstaller.STATUS_PENDING_USER_ACTION) {
            Intent confirm;
            if (Build.VERSION.SDK_INT >= 33) {
                confirm = intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent.class);
            } else {
                confirm = intent.getParcelableExtra(Intent.EXTRA_INTENT);
            }
            if (confirm != null) {
                confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                try {
                    context.startActivity(confirm);
                } catch (Throwable t) {
                    DiagnosticsLog.append(context, "error", "android_update", "launch_installer",
                            t.getClass().getSimpleName(), t.getMessage());
                    Toast.makeText(context, "无法拉起系统安装界面：" + t.getMessage(), Toast.LENGTH_LONG).show();
                }
            }
            return;
        }

        if (status == PackageInstaller.STATUS_SUCCESS) {
            DiagnosticsLog.append(context, "info", "android_update", "install", "OK", "APK install succeeded");
            Toast.makeText(context, "MangaAlpha 更新安装完成", Toast.LENGTH_SHORT).show();
            return;
        }

        if (status == PackageInstaller.STATUS_FAILURE_ABORTED) {
            DiagnosticsLog.append(context, "info", "android_update", "install", "ABORTED", "User canceled install");
            Toast.makeText(context, "已取消更新安装", Toast.LENGTH_SHORT).show();
            return;
        }

        String hint;
        if (status == PackageInstaller.STATUS_FAILURE_CONFLICT || status == PackageInstaller.STATUS_FAILURE_INCOMPATIBLE) {
            hint = "安装包签名冲突或版本不兼容（请确认当前安装的为官方正式签名包）";
        } else if (status == PackageInstaller.STATUS_FAILURE_STORAGE) {
            hint = "设备存储空间不足，无法安装更新";
        } else if (msg != null && !msg.isEmpty()) {
            hint = "安装失败：" + msg;
        } else {
            hint = "安装失败（状态码 " + status + "）";
        }
        DiagnosticsLog.append(context, "error", "android_update", "install", "STATUS_" + status,
                msg != null ? msg : hint);
        Toast.makeText(context, hint, Toast.LENGTH_LONG).show();
    }
}
