package io.github.xjogger.fakekomga115;

import android.content.Context;
import android.os.Build;

import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Date;
import java.util.List;
import java.util.Locale;
import java.util.TimeZone;
import java.util.regex.Pattern;

import mobile.Instance;
import mobile.Mobile;

final class DiagnosticsLog {
    private static final long MAX_ANDROID_BYTES = 512L * 1024L;
    private static final Object LOCK = new Object();

    private static final Pattern RE_URL = Pattern.compile("(?i)\\b(?:https?|socks5h?|content|file)://\\S+");
    private static final Pattern RE_PATH = Pattern.compile("/(?:storage|sdcard|data/user|data/data|mnt|home|Users)/[^\\s\"'<>:,;)]+");
    private static final Pattern RE_SECRET = Pattern.compile("(?i)\\b(token|password|passwd|secret|apikey|api_key|cookie|authorization|session)\\s*[:=]\\s*\\S+");

    private DiagnosticsLog() {}

    static File dataDir(Context context) {
        return new File(context.getFilesDir(), "library");
    }

    static File diagDir(Context context) {
        return new File(dataDir(context), "diagnostics");
    }

    static String sanitize(String raw) {
        if (raw == null) return "";
        String s = raw.replace('\r', ' ').replace('\n', ' ').trim();
        if (s.isEmpty()) return "";
        s = RE_URL.matcher(s).replaceAll("[redacted-url]");
        s = RE_SECRET.matcher(s).replaceAll("$1=[redacted]");
        s = RE_PATH.matcher(s).replaceAll("[redacted-path]");
        if (s.length() > 280) s = s.substring(0, 280);
        return s;
    }

    static void append(Context context, String level, String stage, String action, String code, String detail) {
        if (context == null) return;
        try {
            File dir = diagDir(context);
            if (!dir.exists() && !dir.mkdirs()) return;
            File file = new File(dir, "android-events.jsonl");

            SimpleDateFormat fmt = new SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US);
            fmt.setTimeZone(TimeZone.getTimeZone("UTC"));
            JSONObject obj = new JSONObject();
            obj.put("ts", fmt.format(new Date()));
            obj.put("level", level == null ? "info" : level);
            obj.put("component", "android");
            obj.put("stage", stage == null ? "" : stage);
            if (action != null && !action.isEmpty()) obj.put("action", action);
            if (code != null && !code.isEmpty()) obj.put("code", code);
            String clean = sanitize(detail);
            if (!clean.isEmpty()) obj.put("detail", clean);
            byte[] line = (obj.toString() + "\n").getBytes(StandardCharsets.UTF_8);

            synchronized (LOCK) {
                if (file.exists() && file.length() + line.length > MAX_ANDROID_BYTES) {
                    File backup = new File(dir, "android-events.jsonl.1");
                    if (backup.exists()) backup.delete();
                    file.renameTo(backup);
                }
                try (FileOutputStream fos = new FileOutputStream(file, true)) {
                    fos.write(line);
                }
            }
        } catch (Exception ignored) {
            // Never crash the app because of diagnostics I/O.
        }
    }

    static String buildExportText(Context context, Instance instance) {
        StringBuilder sb = new StringBuilder();
        sb.append("# MangaAlpha Android Diagnostics\n");
        sb.append("androidSdk=").append(Build.VERSION.SDK_INT)
          .append(" release=").append(Build.VERSION.RELEASE)
          .append(" abi=").append(Build.SUPPORTED_ABIS.length > 0 ? Build.SUPPORTED_ABIS[0] : "unknown")
          .append('\n');

        String coreReport = null;
        if (instance != null) {
            try {
                coreReport = instance.exportDiagnostics();
            } catch (Throwable ignored) {}
        }
        if (coreReport == null) {
            try {
                coreReport = Mobile.exportDiagnosticsFromDir(dataDir(context).getAbsolutePath());
            } catch (Throwable ignored) {}
        }
        if (coreReport != null && !coreReport.isEmpty()) {
            sb.append('\n').append(coreReport);
            return sb.toString();
        }

        // Pure-Java fallback when Go runtime is unavailable (e.g. startup/LinkageError).
        File dir = diagDir(context);
        List<String> coreLines = new ArrayList<>();
        readTail(new File(dir, "events.jsonl.1"), coreLines, 200);
        readTail(new File(dir, "events.jsonl"), coreLines, 300);
        sb.append("\n## Core Events (").append(coreLines.size()).append(")\n");
        for (String line : coreLines) {
            sb.append(sanitize(line)).append('\n');
        }
        List<String> androidLines = new ArrayList<>();
        readTail(new File(dir, "android-events.jsonl"), androidLines, 300);
        sb.append("\n## Android Native Events (").append(androidLines.size()).append(")\n");
        for (String line : androidLines) {
            sb.append(sanitize(line)).append('\n');
        }
        return sb.toString();
    }

    static void clearAll(Context context, Instance instance) {
        if (instance != null) {
            try { instance.clearDiagnostics(); } catch (Throwable ignored) {}
        }
        synchronized (LOCK) {
            File dir = diagDir(context);
            String[] names = { "events.jsonl", "events.jsonl.1", "android-events.jsonl", "android-events.jsonl.1" };
            for (String n : names) {
                File f = new File(dir, n);
                if (f.exists()) f.delete();
            }
        }
    }

    private static void readTail(File file, List<String> out, int maxLines) {
        if (file == null || !file.exists()) return;
        List<String> tmp = new ArrayList<>();
        try (BufferedReader br = new BufferedReader(new InputStreamReader(new FileInputStream(file), StandardCharsets.UTF_8))) {
            String line;
            while ((line = br.readLine()) != null) {
                String trimmed = line.trim();
                if (!trimmed.isEmpty()) tmp.add(trimmed);
            }
        } catch (Exception ignored) {
            return;
        }
        int start = Math.max(0, tmp.size() - maxLines);
        for (int i = start; i < tmp.size(); i++) {
            out.add(tmp.get(i));
        }
    }
}
