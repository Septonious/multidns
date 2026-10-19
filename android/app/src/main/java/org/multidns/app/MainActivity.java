package org.multidns.app;

import android.Manifest;
import android.app.*;
import android.content.*;
import android.content.pm.*;
import android.net.*;
import android.os.*;
import android.text.InputType;
import android.view.*;
import android.widget.*;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.util.*;
import mobile.Mobile;
import org.json.*;

public final class MainActivity extends Activity {
  private JSONArray profiles = new JSONArray();
  private int selected = -1;
  private LinearLayout page, root;
  private TextView dashboard, scanStatus, scanResults;
  private EditText ranges, workers, timeout, zone;
  private CheckBox txtFilter, nsFilter;
  private final Handler handler = new Handler(Looper.getMainLooper());
  private String pendingProfile, exportText;
  private boolean exportingScan;
  private boolean latencyRunning;
  private TextView latencyResults;
  private final Runnable refresh =
      new Runnable() {
        public void run() {
          if (dashboard != null)
            dashboard.setText(TunnelService.state + "\n" + TunnelService.stats);
          if (scanStatus != null)
            scanStatus.setText(
                ScannerService.state
                    + " · "
                    + ScannerService.scanned
                    + " tested · "
                    + ScannerService.working
                    + " tunnel verified");
          if (scanResults != null && txtFilter != null)
            try {
              scanResults.setText(
                  ScannerService.display(
                      MainActivity.this, txtFilter.isChecked(), nsFilter.isChecked(), true));
            } catch (Exception e) {
              scanStatus.setText(e.getMessage());
            }
          handler.postDelayed(this, 1000);
        }
      };

  @Override
  public void onCreate(Bundle saved) {
    super.onCreate(saved);
    getWindow().setStatusBarColor(0xff142b41);
    getWindow().setNavigationBarColor(0xff142b41);
    try {
      profiles = ProfileStore.load(this);
      if (profiles.length() > 0) selected = 0;
    } catch (Exception e) {
      error("Unable to decrypt saved profiles: " + e.getMessage());
    }
    layout();
    servers();
    if (Build.VERSION.SDK_INT >= 33)
      requestPermissions(new String[] {Manifest.permission.POST_NOTIFICATIONS}, 99);
    handler.post(refresh);
  }

  private int dp(int n) {
    return (int) (n * getResources().getDisplayMetrics().density);
  }

  private void layout() {
    root = new LinearLayout(this);
    root.setOrientation(LinearLayout.VERTICAL);
    root.setBackgroundColor(0xfff2f5f8);
    root.setPadding(dp(12), dp(20), dp(12), dp(8));
    TextView title = new TextView(this);
    title.setText("MultiDNS  /  testing prototype");
    title.setTextSize(22);
    title.setTextColor(0xff142b41);
    root.addView(title);
    LinearLayout nav = new LinearLayout(this);
    for (String name : new String[] {"Servers", "Scanner", "Routing"}) {
      Button b = new Button(this);
      b.setText(name);
      b.setOnClickListener(
          v -> {
            if (name.equals("Servers")) servers();
            else if (name.equals("Scanner")) scanner();
            else routing();
          });
      nav.addView(b, new LinearLayout.LayoutParams(0, -2, 1));
    }
    root.addView(nav);
    ScrollView scroll = new ScrollView(this);
    page = new LinearLayout(this);
    page.setOrientation(LinearLayout.VERTICAL);
    scroll.addView(page);
    root.addView(scroll, new LinearLayout.LayoutParams(-1, 0, 1));
    setContentView(root);
  }

  private void clear() {
    page.removeAllViews();
    dashboard = null;
    scanStatus = null;
    scanResults = null;
    latencyResults = null;
  }

  private TextView text(LinearLayout parent, String content) {
    TextView t = new TextView(this);
    t.setText(content);
    t.setTextSize(15);
    t.setPadding(dp(4), dp(9), dp(4), dp(9));
    t.setTextColor(0xff19364e);
    parent.addView(t);
    return t;
  }

  private void button(LinearLayout parent, String title, Runnable action) {
    Button b = new Button(this);
    b.setText(title);
    b.setOnClickListener(
        v -> {
          try {
            action.run();
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
    parent.addView(b);
  }

  private EditText field(LinearLayout parent, String label, String value, boolean multiline) {
    text(parent, label);
    EditText e = new EditText(this);
    e.setText(value);
    e.setTextSize(14);
    e.setSingleLine(!multiline);
    if (multiline) {
      e.setMinLines(3);
      e.setGravity(Gravity.TOP);
      e.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE);
    }
    parent.addView(e, new LinearLayout.LayoutParams(-1, -2));
    return e;
  }

  private JSONObject profile() {
    return profiles.optJSONObject(selected);
  }

  private void save() {
    try {
      ProfileStore.save(this, profiles);
    } catch (Exception e) {
      throw new IllegalStateException(e);
    }
  }

  private void servers() {
    clear();
    text(
        page,
        "Resolver groups race identical requests. Different groups carry independent work. Profiles"
            + " and authentication keys are encrypted on this device.");
    dashboard = text(page, TunnelService.state);
    if (profiles.length() == 0)
      text(page, "Add your server to begin. You need a delegated domain and the server key.");
    for (int i = 0; i < profiles.length(); i++) {
      final int index = i;
      JSONObject p = profiles.optJSONObject(i);
      button(
          page,
          (i == selected ? "● " : "○ ") + p.optString("name") + " · " + p.optString("domain"),
          () -> {
            selected = index;
            servers();
          });
    }
    button(page, "Add server", () -> edit(-1));
    button(
        page,
        "Import profile JSON",
        () -> {
          Intent i =
              new Intent(Intent.ACTION_OPEN_DOCUMENT)
                  .setType("application/json")
                  .addCategory(Intent.CATEGORY_OPENABLE);
          startActivityForResult(i, 42);
        });
    if (profile() != null) {
      button(
          page,
          "Connect selected server",
          () -> {
            pendingProfile = profile().toString();
            Intent permission = android.net.VpnService.prepare(this);
            if (permission != null) startActivityForResult(permission, 41);
            else connect();
          });
      button(page, "Edit server and resolver groups", () -> edit(selected));
      button(page, "Test all resolver latencies (VPN off)", this::latencyTest);
      button(
          page,
          "Export profile without key",
          () -> {
            try {
              JSONObject p = new JSONObject(profile().toString());
              p.remove("key");
              exportText = p.toString(2);
              exportingScan = false;
              createDocument("multidns-profile.json", "application/json", 43);
            } catch (Exception e) {
              error(e.getMessage());
            }
          });
      button(
          page,
          "Duplicate server",
          () -> {
            try {
              JSONObject p = new JSONObject(profile().toString());
              p.put("name", p.optString("name") + " copy");
              profiles.put(p);
              selected = profiles.length() - 1;
              save();
              servers();
            } catch (Exception e) {
              error(e.getMessage());
            }
          });
      button(
          page,
          "Delete selected server",
          () ->
              new AlertDialog.Builder(this)
                  .setTitle("Delete server?")
                  .setMessage(profile().optString("name"))
                  .setPositiveButton(
                      "Delete",
                      (d, w) -> {
                        profiles.remove(selected);
                        selected = profiles.length() > 0 ? 0 : -1;
                        save();
                        servers();
                      })
                  .setNegativeButton("Cancel", null)
                  .show());
    }
    button(
        page,
        "Disconnect",
        () -> {
          startService(new Intent(this, TunnelService.class).setAction("STOP"));
        });
  }

  private void startManagedService(Intent intent) {
    if (Build.VERSION.SDK_INT >= 26) startForegroundService(intent);
    else startService(intent);
  }

  private void connect() {
    if (TunnelService.instance != null) {
      error("Disconnect before switching servers.");
      return;
    }
    Intent i = new Intent(this, TunnelService.class).putExtra("profile", pendingProfile);
    startManagedService(i);
  }

  private JSONObject empty() {
    try {
      return new JSONObject(
          "{\"name\":\"My"
              + " server\",\"domain\":\"\",\"client_id\":\"phone\",\"key\":\"\",\"groups\":[],\"rules\":[],\"default_action\":\"tunnel\",\"mode\":\"stripe\",\"app_mode\":\"all\",\"apps\":[],\"window\":8,\"timeout_ms\":1500,\"adaptive\":true,\"automatic_groups\":false,\"group_size\":2,\"group_count\":5}");
    } catch (Exception e) {
      throw new RuntimeException(e);
    }
  }

  private void edit(int index) {
    clear();
    JSONObject original = index >= 0 ? profiles.optJSONObject(index) : empty();
    EditText name = field(page, "Server name", original.optString("name"), false),
        domain = field(page, "Delegated tunnel domain", original.optString("domain"), false),
        client = field(page, "Client ID", original.optString("client_id", "phone"), false),
        key = field(page, "Server authentication key", original.optString("key"), false);
    key.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
    text(
        page,
        "One group per line, members separated by commas. Up to 100 active resolvers. For five"
            + " groups of two, enter five lines with two IPs each.");
    StringBuilder groups = new StringBuilder();
    JSONArray gs = original.optJSONArray("groups");
    for (int i = 0; gs != null && i < gs.length(); i++) {
      JSONArray rs = gs.optJSONObject(i).optJSONArray("resolvers");
      for (int j = 0; rs != null && j < rs.length(); j++) {
        if (j > 0) groups.append(", ");
        groups.append(rs.optString(j));
      }
      groups.append("\n");
    }
    EditText members = field(page, "Resolver groups", groups.toString(), true);
    EditText size = field(page, "Auto-group: resolvers per group", "2", false);
    size.setInputType(InputType.TYPE_CLASS_NUMBER);
    button(
        page,
        "Regroup all entered resolvers",
        () -> {
          try {
            int n = Integer.parseInt(size.getText().toString());
            if (n < 1 || n > 100) throw new Exception("Group size must be 1..100");
            String[] tokens = members.getText().toString().trim().split("[\\s,]+");
            LinkedHashSet<String> unique = new LinkedHashSet<>(Arrays.asList(tokens));
            StringBuilder out = new StringBuilder();
            int i = 0;
            for (String r : unique) {
              if (r.trim().isEmpty()) continue;
              if (i > 0) out.append(i % n == 0 ? '\n' : ',');
              out.append(r);
              i++;
            }
            members.setText(out.toString());
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
    CheckBox adaptive = new CheckBox(this);
    adaptive.setText("Adapt paths: low RTT for short requests, throughput for bulk");
    adaptive.setChecked(original.optBoolean("adaptive", true));
    page.addView(adaptive);
    CheckBox automatic = new CheckBox(this);
    automatic.setText("Automatically form groups on each connection");
    automatic.setChecked(original.optBoolean("automatic_groups", false));
    page.addView(automatic);
    EditText
        groupSize =
            field(
                page,
                "Automatic: resolvers per group",
                String.valueOf(original.optInt("group_size", 2)),
                false),
        groupCount =
            field(
                page,
                "Automatic: maximum groups",
                String.valueOf(original.optInt("group_count", 5)),
                false);
    groupSize.setInputType(InputType.TYPE_CLASS_NUMBER);
    groupCount.setInputType(InputType.TYPE_CLASS_NUMBER);
    text(
        page,
        "Automatic mode tests all candidates concurrently before connecting. Excess healthy"
            + " resolvers stay as spares; a final group can contain fewer members. Health and group"
            + " membership are refreshed while connected.");
    EditText candidatePool =
        field(
            page,
            "Additional candidate / spare resolvers, one per line",
            joinArray(original.optJSONArray("resolver_pool")),
            true);
    LinearLayout board = new LinearLayout(this);
    board.setOrientation(LinearLayout.VERTICAL);
    page.addView(board);
    ArrayList<ArrayList<String>> boardGroups = parseGroupText(members.getText().toString());
    ArrayList<String> standby = new ArrayList<>();
    for (String token : candidatePool.getText().toString().split("[\\s,]+"))
      if (!token.trim().isEmpty()) standby.add(token);
    text(
        page,
        "Manual group board: long press a resolver card, then drag it into a group or the spare"
            + " pool.");
    renderBoard(board, members, candidatePool, boardGroups, standby);
    button(
        page,
        "Refresh cards from text",
        () -> {
          boardGroups.clear();
          boardGroups.addAll(parseGroupText(members.getText().toString()));
          standby.clear();
          for (String token : candidatePool.getText().toString().split("[\\s,]+"))
            if (!token.trim().isEmpty()) standby.add(token);
          renderBoard(board, members, candidatePool, boardGroups, standby);
        });
    button(
        page,
        "Add empty group to board",
        () -> {
          boardGroups.add(new ArrayList<>());
          renderBoard(board, members, candidatePool, boardGroups, standby);
        });
    Spinner mode = new Spinner(this);
    mode.setAdapter(
        new ArrayAdapter<>(
            this,
            android.R.layout.simple_spinner_dropdown_item,
            new String[] {"stripe", "affinity"}));
    mode.setSelection(original.optString("mode").equals("affinity") ? 1 : 0);
    text(page, "Scheduling mode");
    page.addView(mode);
    EditText window =
        field(
            page,
            "Outstanding requests per stream (1–32)",
            "" + original.optInt("window", 8),
            false);
    window.setInputType(InputType.TYPE_CLASS_NUMBER);
    button(
        page,
        "Save server",
        () -> {
          try {
            JSONObject p = new JSONObject(original.toString());
            p.put("name", name.getText().toString().trim());
            p.put("domain", domain.getText().toString().trim());
            p.put("client_id", client.getText().toString().trim());
            p.put("key", key.getText().toString());
            p.put("mode", mode.getSelectedItem().toString());
            p.put("window", Integer.parseInt(window.getText().toString()));
            p.put("adaptive", adaptive.isChecked());
            p.put("automatic_groups", automatic.isChecked());
            p.put("group_size", Integer.parseInt(groupSize.getText().toString()));
            p.put("group_count", Integer.parseInt(groupCount.getText().toString()));
            JSONArray pool = new JSONArray();
            for (String token : candidatePool.getText().toString().split("[\\s,]+"))
              if (!token.trim().isEmpty()) pool.put(token);
            p.put("resolver_pool", pool);
            JSONArray newGroups = new JSONArray();
            int gi = 1;
            for (String line : members.getText().toString().split("\\n")) {
              if (line.trim().isEmpty()) continue;
              JSONArray resolvers = new JSONArray();
              for (String r : line.trim().split("[\\s,]+")) resolvers.put(r);
              newGroups.put(
                  new JSONObject().put("name", "Group " + gi++).put("resolvers", resolvers));
            }
            p.put("groups", newGroups);
            Mobile.validate(p.toString());
            if (index >= 0) profiles.put(index, p);
            else {
              profiles.put(p);
              selected = profiles.length() - 1;
            }
            save();
            servers();
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
    button(page, "Cancel", this::servers);
  }

  private String joinArray(JSONArray a) {
    ArrayList<String> list = new ArrayList<>();
    for (int i = 0; a != null && i < a.length(); i++) list.add(a.optString(i));
    return android.text.TextUtils.join("\n", list);
  }

  private ArrayList<ArrayList<String>> parseGroupText(String input) {
    ArrayList<ArrayList<String>> out = new ArrayList<>();
    for (String line : input.split("\n")) {
      if (line.trim().isEmpty()) continue;
      ArrayList<String> g = new ArrayList<>();
      for (String token : line.trim().split("[\\s,]+")) if (!token.trim().isEmpty()) g.add(token);
      out.add(g);
    }
    return out;
  }

  private void renderBoard(
      LinearLayout board,
      EditText members,
      EditText pool,
      ArrayList<ArrayList<String>> groups,
      ArrayList<String> standby) {
    board.removeAllViews();
    for (int index = -1; index < groups.size(); index++) {
      final int destination = index;
      LinearLayout box = new LinearLayout(this);
      box.setOrientation(LinearLayout.VERTICAL);
      box.setPadding(dp(8), dp(8), dp(8), dp(12));
      box.setBackgroundColor(index < 0 ? 0xffdce6ee : 0xffe0eee6);
      LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(-1, -2);
      params.setMargins(0, dp(6), 0, dp(6));
      board.addView(box, params);
      text(box, index < 0 ? "Spare / unassigned pool" : "Group " + (index + 1));
      ArrayList<String> list = index < 0 ? standby : groups.get(index);
      for (String resolver : new ArrayList<>(list)) {
        String measured = "";
        try {
          JSONObject h =
              new JSONObject(getSharedPreferences("latency", 0).getString(resolver, "{}"));
          if (h.has("healthy"))
            measured =
                h.optBoolean("healthy")
                    ? String.format(java.util.Locale.ROOT, " · %.1f ms", h.optDouble("rtt_ms"))
                    : " · not working";
        } catch (Exception ignored) {
        }
        TextView card = text(box, "▦  " + resolver + measured + "  · hold to drag");
        card.setBackgroundColor(0xffffffff);
        card.setPadding(dp(12), dp(12), dp(12), dp(12));
        card.setOnLongClickListener(
            v -> {
              v.startDragAndDrop(
                  ClipData.newPlainText("resolver", resolver),
                  new View.DragShadowBuilder(v),
                  resolver,
                  0);
              return true;
            });
      }
      box.setOnDragListener(
          (v, event) -> {
            switch (event.getAction()) {
              case DragEvent.ACTION_DRAG_STARTED:
                return event.getLocalState() instanceof String;
              case DragEvent.ACTION_DRAG_ENTERED:
                v.setAlpha(.6f);
                return true;
              case DragEvent.ACTION_DRAG_EXITED:
              case DragEvent.ACTION_DRAG_ENDED:
                v.setAlpha(1f);
                return true;
              case DragEvent.ACTION_DROP:
                String resolver = (String) event.getLocalState();
                standby.remove(resolver);
                for (ArrayList<String> g : groups) g.remove(resolver);
                if (destination < 0) standby.add(resolver);
                else groups.get(destination).add(resolver);
                StringBuilder b = new StringBuilder();
                for (ArrayList<String> g : groups)
                  if (!g.isEmpty()) b.append(android.text.TextUtils.join(", ", g)).append("\n");
                members.setText(b.toString());
                pool.setText(android.text.TextUtils.join("\n", standby));
                renderBoard(board, members, pool, groups, standby);
                return true;
              default:
                return true;
            }
          });
    }
  }

  private void latencyTest() {
    if (TunnelService.instance != null) {
      error("Disconnect the VPN before running the manual latency test.");
      return;
    }
    if (latencyRunning) {
      error("Latency test is already running.");
      return;
    }
    if (profile() == null) return;
    clear();
    text(page, "Concurrent authenticated tunnel probes · VPN remains off");
    latencyResults = text(page, "Testing all candidates…\n");
    final TextView output = latencyResults;
    final String config = profile().toString();
    latencyRunning = true;
    new Thread(
            () -> {
              try {
                mobile.Engine engine = Mobile.newEngine();
                engine.testLatency(
                    config,
                    message -> {
                      try {
                        JSONObject r = new JSONObject(message);
                        getSharedPreferences("latency", 0)
                            .edit()
                            .putString(r.getString("endpoint"), message)
                            .apply();
                        String line =
                            r.getString("endpoint")
                                + "  "
                                + (r.optBoolean("healthy")
                                    ? String.format(
                                        java.util.Locale.ROOT, "%.1f ms", r.optDouble("rtt_ms"))
                                    : "not working")
                                + "\n";
                        runOnUiThread(() -> output.append(line));
                      } catch (Exception ignored) {
                      }
                    });
                runOnUiThread(
                    () -> output.append("Finished. Automatic mode measures again on startup.\n"));
              } catch (Exception e) {
                runOnUiThread(() -> error(e.getMessage()));
              } finally {
                latencyRunning = false;
              }
            },
            "multidns-latency")
        .start();
  }

  private void routing() {
    clear();
    if (profile() == null) {
      text(page, "Add a server first.");
      return;
    }
    JSONObject p = profile();
    text(
        page,
        "App selection is applied first. Excluded apps bypass destination rules. For captured"
            + " traffic, the first matching rule wins; unmatched connections use the default"
            + " action. Reconnect after editing routing.");
    String[] modes = {"all", "include", "exclude"};
    Spinner appMode = new Spinner(this);
    appMode.setAdapter(
        new ArrayAdapter<>(this, android.R.layout.simple_spinner_dropdown_item, modes));
    appMode.setSelection(Arrays.asList(modes).indexOf(p.optString("app_mode", "all")));
    page.addView(appMode);
    final LinkedHashSet<String> apps = new LinkedHashSet<>();
    JSONArray saved = p.optJSONArray("apps");
    for (int i = 0; saved != null && i < saved.length(); i++) apps.add(saved.optString(i));
    TextView selectedApps =
        text(page, apps.isEmpty() ? "No selected apps" : android.text.TextUtils.join("\n", apps));
    button(
        page,
        "Select installed apps",
        () -> {
          Intent launch = new Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER);
          List<ResolveInfo> list = getPackageManager().queryIntentActivities(launch, 0);
          TreeMap<String, String> packages = new TreeMap<>();
          for (ResolveInfo info : list)
            packages.put(
                info.activityInfo.packageName, info.loadLabel(getPackageManager()).toString());
          String[] ids = packages.keySet().toArray(new String[0]);
          String[] labels = new String[ids.length];
          boolean[] checked = new boolean[ids.length];
          for (int i = 0; i < ids.length; i++) {
            labels[i] = packages.get(ids[i]) + "\n" + ids[i];
            checked[i] = apps.contains(ids[i]);
          }
          new AlertDialog.Builder(this)
              .setTitle("Apps to include or exclude")
              .setMultiChoiceItems(
                  labels,
                  checked,
                  (d, which, on) -> {
                    if (on) apps.add(ids[which]);
                    else apps.remove(ids[which]);
                  })
              .setPositiveButton(
                  "Done", (d, w) -> selectedApps.setText(android.text.TextUtils.join("\n", apps)))
              .setNegativeButton("Cancel", null)
              .show();
        });
    EditText appIds =
        field(
            page,
            "Additional package IDs, one per line (for apps without launcher icons)",
            "",
            true);
    String[] actions = {"tunnel", "direct", "block"};
    Spinner fallback = new Spinner(this);
    fallback.setAdapter(
        new ArrayAdapter<>(this, android.R.layout.simple_spinner_dropdown_item, actions));
    fallback.setSelection(Arrays.asList(actions).indexOf(p.optString("default_action", "tunnel")));
    text(page, "Default action");
    page.addView(fallback);
    text(
        page,
        "Rules are ordered JSON objects. Examples:\n"
            + "{\"domain\":\"example.org\",\"action\":\"direct\"}\n"
            + "{\"cidr\":\"192.168.0.0/16\",\"action\":\"direct\"}\n"
            + "{\"port\":443,\"protocol\":\"udp\",\"action\":\"block\"}\n"
            + "Optional exact:true matches one domain. Domain rules rely on observed DNS; encrypted"
            + " external DNS can hide names.");
    EditText rules =
        field(
            page,
            "Ordered rules",
            p.optJSONArray("rules") == null ? "[]" : p.optJSONArray("rules").toString(),
            true);
    rules.setMinLines(8);
    button(
        page,
        "Save routing",
        () -> {
          try {
            JSONObject copy = new JSONObject(p.toString());
            copy.put("app_mode", appMode.getSelectedItem().toString());
            for (String id : appIds.getText().toString().split("\\s+"))
              if (!id.trim().isEmpty()) apps.add(id.trim());
            copy.put("apps", new JSONArray(apps));
            copy.put("default_action", fallback.getSelectedItem().toString());
            copy.put("rules", new JSONArray(rules.getText().toString()));
            Mobile.validate(copy.toString());
            profiles.put(selected, copy);
            save();
            Toast.makeText(this, "Saved. Reconnect to apply.", Toast.LENGTH_LONG).show();
            servers();
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
  }

  private void scanner() {
    clear();
    if (profile() == null) {
      text(page, "Add a server profile so the scanner can verify the actual tunnel.");
      return;
    }
    text(
        page,
        "Results stream live and are stored on disk. Arbitrary numbers of ranges are processed"
            + " lazily. IPv6 ranges are exhaustive; choose narrow ranges. Scanner sockets bypass"
            + " this VPN.");
    android.content.SharedPreferences prefs = getSharedPreferences("scanner", 0);
    ranges =
        field(
            page,
            "IPs, CIDRs, or start–end ranges",
            prefs.getString("ranges", "1.1.1.1\n8.8.8.8"),
            true);
    workers = field(page, "DNS workers (1–256)", prefs.getString("workers", "16"), false);
    timeout =
        field(page, "Timeout per test, milliseconds", prefs.getString("timeout", "1500"), false);
    zone = field(page, "NS probe zone (optional)", prefs.getString("zone", ""), false);
    workers.setInputType(InputType.TYPE_CLASS_NUMBER);
    timeout.setInputType(InputType.TYPE_CLASS_NUMBER);
    txtFilter = new CheckBox(this);
    txtFilter.setText("Only TXT verified");
    txtFilter.setChecked(true);
    page.addView(txtFilter);
    nsFilter = new CheckBox(this);
    nsFilter.setText("Also require NS → A");
    page.addView(nsFilter);
    scanStatus = text(page, ScannerService.state);
    scanResults = text(page, "");
    scanResults.setTextIsSelectable(true);
    button(
        page,
        "Start scan",
        () -> {
          try {
            if (ScannerService.running) throw new Exception("Stop the current scan first");
            prefs
                .edit()
                .putString("ranges", ranges.getText().toString())
                .putString("workers", workers.getText().toString())
                .putString("timeout", timeout.getText().toString())
                .putString("zone", zone.getText().toString())
                .apply();
            JSONObject o =
                new JSONObject()
                    .put("ranges", ranges.getText().toString())
                    .put("workers", Integer.parseInt(workers.getText().toString()))
                    .put("timeout_ms", Integer.parseInt(timeout.getText().toString()))
                    .put("ns_zone", zone.getText().toString().trim())
                    .put("profile", profile());
            startManagedService(
                new Intent(this, ScannerService.class).putExtra("options", o.toString()));
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
    button(
        page,
        "Resume paused scan",
        () -> {
          try {
            if (ScannerService.running) throw new Exception("Pause the current scan first");
            JSONObject o =
                new JSONObject()
                    .put("ranges", ranges.getText().toString())
                    .put("workers", Integer.parseInt(workers.getText().toString()))
                    .put("timeout_ms", Integer.parseInt(timeout.getText().toString()))
                    .put("ns_zone", zone.getText().toString().trim())
                    .put("profile", profile());
            startManagedService(
                new Intent(this, ScannerService.class)
                    .putExtra("options", o.toString())
                    .putExtra("resume", true));
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
    button(
        page,
        "Pause / stop scan",
        () -> startService(new Intent(this, ScannerService.class).setAction("STOP")));
    button(
        page,
        "Copy all matching IPs, one per line",
        () -> {
          try {
            String s =
                ScannerService.export(
                    this, txtFilter.isChecked(), nsFilter.isChecked(), true, 500000);
            ((android.content.ClipboardManager) getSystemService(CLIPBOARD_SERVICE))
                .setPrimaryClip(ClipData.newPlainText("DNS resolvers", s));
            Toast.makeText(this, "Copied", Toast.LENGTH_SHORT).show();
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
    button(
        page,
        "Export matching resolver list",
        () -> {
          exportingScan = true;
          createDocument("multidns-resolvers.txt", "text/plain", 43);
        });
    button(
        page,
        "Add matching results to selected profile",
        () -> {
          try {
            String s =
                ScannerService.export(
                    this, txtFilter.isChecked(), nsFilter.isChecked(), true, 10000);
            JSONObject p = new JSONObject(profile().toString());
            JSONArray groups = p.getJSONArray("groups");
            Set<String> existing = new HashSet<>();
            for (int i = 0; i < groups.length(); i++) {
              JSONArray a = groups.getJSONObject(i).getJSONArray("resolvers");
              for (int j = 0; j < a.length(); j++) existing.add(a.getString(j));
            }
            int added = 0;
            for (String ip : s.split("\n")) {
              if (ip.trim().isEmpty() || existing.contains(ip) || existing.contains(ip + ":53"))
                continue;
              if (existing.size() >= 100) break;
              groups.put(
                  new JSONObject()
                      .put("name", "Scanned " + (groups.length() + 1))
                      .put("resolvers", new JSONArray().put(ip)));
              existing.add(ip);
              added++;
            }
            Mobile.validate(p.toString());
            profiles.put(selected, p);
            save();
            Toast.makeText(
                    this,
                    "Added " + added + " resolvers. Edit server to regroup.",
                    Toast.LENGTH_LONG)
                .show();
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
  }

  private void createDocument(String name, String type, int code) {
    startActivityForResult(
        new Intent(Intent.ACTION_CREATE_DOCUMENT)
            .setType(type)
            .addCategory(Intent.CATEGORY_OPENABLE)
            .putExtra(Intent.EXTRA_TITLE, name),
        code);
  }

  @Override
  protected void onActivityResult(int request, int result, Intent data) {
    super.onActivityResult(request, result, data);
    if (result != RESULT_OK) return;
    try {
      if (request == 41) {
        connect();
      } else if (request == 42 && data != null) {
        try (InputStream in = getContentResolver().openInputStream(data.getData())) {
          ByteArrayOutputStream b = new ByteArrayOutputStream();
          byte[] chunk = new byte[8192];
          int n;
          while ((n = in.read(chunk)) != -1) {
            if (b.size() + n > 1024 * 1024) throw new Exception("Profile is too large");
            b.write(chunk, 0, n);
          }
          JSONObject p = new JSONObject(b.toString(StandardCharsets.UTF_8.name()));
          if (!p.has("key")) {
            p.put("key", "");
            profiles.put(p);
            selected = profiles.length() - 1;
            edit(selected);
            return;
          }
          Mobile.validate(p.toString());
          profiles.put(p);
          selected = profiles.length() - 1;
          save();
          servers();
        }
      } else if (request == 43 && data != null) {
        try (OutputStream out = getContentResolver().openOutputStream(data.getData())) {
          if (exportingScan) {
            ScannerService.writeExport(
                this,
                txtFilter.isChecked(),
                nsFilter.isChecked(),
                true,
                s -> out.write(s.getBytes(StandardCharsets.UTF_8)));
          } else out.write(exportText.getBytes(StandardCharsets.UTF_8));
        }
        Toast.makeText(this, "Exported", Toast.LENGTH_SHORT).show();
      }
    } catch (Exception e) {
      error(e.getMessage());
    }
  }

  private void error(String message) {
    new AlertDialog.Builder(this)
        .setTitle("MultiDNS")
        .setMessage(message == null ? "Unknown error" : message)
        .setPositiveButton("OK", null)
        .show();
  }

  @Override
  protected void onDestroy() {
    handler.removeCallbacks(refresh);
    super.onDestroy();
  }
}
