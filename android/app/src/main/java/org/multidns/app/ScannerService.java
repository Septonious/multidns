package org.multidns.app;

import android.app.*;
import android.content.*;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.os.IBinder;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import mobile.Engine;
import mobile.Mobile;
import org.json.*;

public final class ScannerService extends Service {
  static volatile String state = "Idle";
  static volatile long scanned, working;
  static volatile boolean running;
  static final ArrayList<String> recent = new ArrayList<>();
  private Engine engine;
  private long nextIndex;
  private final java.util.TreeSet<Long> completed = new java.util.TreeSet<>();
  private static final Object DB_LOCK = new Object();

  private static SQLiteDatabase db(Context c) {
    SQLiteDatabase db = c.openOrCreateDatabase("scans.db", MODE_PRIVATE, null);
    db.execSQL(
        "CREATE TABLE IF NOT EXISTS results(endpoint TEXT PRIMARY KEY,txt INTEGER,ns INTEGER,tunnel"
            + " INTEGER,rtt INTEGER,json TEXT)");
    db.execSQL("CREATE INDEX IF NOT EXISTS results_all ON results(tunnel,rtt)");
    db.execSQL("CREATE INDEX IF NOT EXISTS results_txt ON results(tunnel,txt,rtt)");
    db.execSQL("CREATE INDEX IF NOT EXISTS results_ns ON results(tunnel,ns,rtt)");
    db.execSQL("CREATE INDEX IF NOT EXISTS results_txt_ns ON results(tunnel,txt,ns,rtt)");
    return db;
  }

  static void clear(Context c) {
    synchronized (DB_LOCK) {
      try (SQLiteDatabase db = db(c)) {
        db.delete("results", null, null);
      }
    }
    scanned = 0;
    working = 0;
    synchronized (recent) {
      recent.clear();
    }
  }

  static String export(Context c, boolean txt, boolean ns, boolean tunnel, int limit)
      throws Exception {
    StringBuilder b = new StringBuilder();
    writeExport(
        c,
        txt,
        ns,
        tunnel,
        (s) -> {
          if (b.length() + s.length() > limit)
            throw new IOException("Result list is too large for clipboard; use Export list");
          b.append(s);
        });
    return b.toString();
  }

  interface LineWriter {
    void write(String s) throws Exception;
  }

  static void writeExport(Context c, boolean txt, boolean ns, boolean tunnel, LineWriter writer)
      throws Exception {
    synchronized (DB_LOCK) {
      try (SQLiteDatabase db = db(c);
          Cursor cursor =
              db.rawQuery(
                  "SELECT endpoint FROM results WHERE tunnel=1"
                      + (txt ? " AND txt=1" : "")
                      + (ns ? " AND ns=1" : "")
                      + (tunnel ? " AND tunnel=1" : "")
                      + " ORDER BY rtt ASC",
                  null)) {
        while (cursor.moveToNext()) {
          String endpoint = cursor.getString(0);
          if (endpoint.endsWith(":53")) {
            endpoint = endpoint.substring(0, endpoint.length() - 3);
            if (endpoint.startsWith("[")) endpoint = endpoint.substring(1, endpoint.length() - 1);
          }
          writer.write(endpoint + "\n");
        }
      }
    }
  }

  static String display(Context c, boolean txt, boolean ns, boolean tunnel) {
    StringBuilder b = new StringBuilder();
    synchronized (DB_LOCK) {
      try (SQLiteDatabase db = db(c);
          Cursor cursor =
              db.rawQuery(
                  "SELECT endpoint,txt,ns,tunnel,rtt FROM results WHERE tunnel=1"
                      + (txt ? " AND txt=1" : "")
                      + (ns ? " AND ns=1" : "")
                      + " ORDER BY rtt ASC LIMIT 150",
                  null)) {
        while (cursor.moveToNext()) {
          b.append(cursor.getString(0))
              .append("  ")
              .append(cursor.getLong(4))
              .append(" ms  TXT ")
              .append(cursor.getInt(1) == 1 ? "✓" : "✗")
              .append("  NS→A ")
              .append(cursor.getInt(2) == 1 ? "✓" : "✗")
              .append("\n");
        }
      }
    }
    return b.toString();
  }

  @Override
  public int onStartCommand(Intent intent, int flags, int id) {
    if (intent == null) return START_NOT_STICKY;
    if ("STOP".equals(intent.getAction())) {
      if (engine != null) engine.stopScan();
      return START_NOT_STICKY;
    }
    if (running) return START_NOT_STICKY;
    startForeground(2, TunnelService.notification(this, "MultiDNS resolver scan", 2));
    running = true;
    state = "Scanning";
    engine = Mobile.newEngine();
    String options = intent.getStringExtra("options");
    boolean resume = intent.getBooleanExtra("resume", false);
    android.content.SharedPreferences prefs = getSharedPreferences("scan-progress", 0);
    try {
      JSONObject parsed = new JSONObject(options);
      String fingerprint =
          android.util.Base64.encodeToString(
              java.security.MessageDigest.getInstance("SHA-256")
                  .digest(options.getBytes(StandardCharsets.UTF_8)),
              android.util.Base64.NO_WRAP);
      if (resume && !fingerprint.equals(prefs.getString("fingerprint", "")))
        throw new Exception("Scan settings changed. Start a new scan instead.");
      nextIndex = resume ? prefs.getLong("next", 0) : 0;
      parsed.put("resume_at", nextIndex);
      options = parsed.toString();
      prefs.edit().putString("fingerprint", fingerprint).putLong("next", nextIndex).apply();
    } catch (Exception e) {
      state = e.getMessage();
      running = false;
      stopSelf();
      return START_NOT_STICKY;
    }
    if (!resume) clear(this);
    final String scanOptions = options;
    new Thread(
            () -> {
              try {
                engine.scan(
                    scanOptions,
                    fd -> {
                      TunnelService vpn = TunnelService.instance;
                      return vpn == null || vpn.protectFD((int) fd);
                    },
                    message -> {
                      try {
                        JSONObject r = new JSONObject(message);
                        synchronized (DB_LOCK) {
                          try (SQLiteDatabase db = db(this)) {
                            ContentValues values = new ContentValues();
                            values.put("endpoint", r.getString("endpoint"));
                            values.put("txt", r.optBoolean("txt") ? 1 : 0);
                            values.put("ns", r.optBoolean("ns_a") ? 1 : 0);
                            values.put("tunnel", r.optBoolean("tunnel") ? 1 : 0);
                            values.put("rtt", r.optLong("rtt_ms"));
                            values.put("json", message);
                            db.insertWithOnConflict(
                                "results", null, values, SQLiteDatabase.CONFLICT_REPLACE);
                          }
                        }
                        synchronized (completed) {
                          long index = r.optLong("index");
                          completed.add(index);
                          while (completed.remove(nextIndex)) nextIndex++;
                          getSharedPreferences("scan-progress", 0)
                              .edit()
                              .putLong("next", nextIndex)
                              .apply();
                        }
                        scanned++;
                        if (r.optBoolean("tunnel")) working++;
                      } catch (Exception e) {
                        state = "Storage error: " + e.getMessage();
                        engine.stopScan();
                      }
                    });
                if (state.equals("Scanning")) state = "Completed";
              } catch (Exception e) {
                state = "Stopped: " + e.getMessage();
              } finally {
                running = false;
                stopSelf();
              }
            },
            "multidns-scanner")
        .start();
    return START_NOT_STICKY;
  }

  @Override
  public void onDestroy() {
    if (engine != null) engine.stopScan();
    super.onDestroy();
  }

  @Override
  public IBinder onBind(Intent intent) {
    return null;
  }
}
