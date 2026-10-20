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
  private ScrollView scroll;
  private TextView downValue, upValue, healthValue, trafficValue;
  private Button connectButton;
  private final ArrayList<TextView> navLabels = new ArrayList<>();
  private final ArrayList<Ui.Icon> navIcons = new ArrayList<>();
  private final ArrayList<LinearLayout> navItems = new ArrayList<>();
  private int currentTab;
  private TextView diagnosticsSummary, tuningOutput;
  private LinearLayout diagnosticsPaths;
  private final HashMap<String, TextView> resolverViews = new HashMap<>();
  private volatile mobile.Engine tuningEngine;
  private volatile boolean tuningRunning;
  private String tuningReport, tuningProfile;
  private Button applyTuning;

  private boolean editing;
  private TextView dashboard, scanStatus, scanResults;
  private EditText ranges, workers, timeout, zone;
  private CheckBox txtFilter, nsFilter;
  private final Handler handler = new Handler(Looper.getMainLooper());
  private String pendingProfile, exportText;
  private boolean exportingScan;
  private boolean latencyRunning;
  private boolean scanRefreshing;
  private TextView latencyResults;
  private long statsAt, statsUploaded, statsDownloaded;
  private final Runnable refresh =
      new Runnable() {
        public void run() {
          if (dashboard != null) updateDashboard();
          if (diagnosticsSummary != null) updateDiagnostics();
          if (scanStatus != null)
            scanStatus.setText(
                ScannerService.state
                    + " · "
                    + ScannerService.scanned
                    + " tested · "
                    + ScannerService.working
                    + " tunnel verified");
          if (scanResults != null && txtFilter != null && !scanRefreshing) {
            scanRefreshing = true;
            final TextView output = scanResults;
            final boolean txt = txtFilter.isChecked(), ns = nsFilter.isChecked();
            new Thread(
                    () -> {
                      String rows;
                      try {
                        rows = ScannerService.display(MainActivity.this, txt, ns, true);
                      } catch (Exception e) {
                        rows = "Unable to read results: " + e.getMessage();
                      }
                      final String snapshot = rows;
                      handler.post(
                          () -> {
                            scanRefreshing = false;
                            if (scanResults == output)
                              output.setText(
                                  snapshot.isEmpty()
                                      ? "Verified resolvers will appear here."
                                      : snapshot);
                          });
                    },
                    "multidns-scan-display")
                .start();
          }
          handler.postDelayed(this, 1000);
        }
      };

  @Override
  public void onCreate(Bundle saved) {
    super.onCreate(saved);
    getWindow().setStatusBarColor(Ui.BG);
    getWindow().setNavigationBarColor(Ui.SURFACE);
    getWindow().getDecorView().setSystemUiVisibility(0);
    try {
      profiles = ProfileStore.load(this);
      if (profiles.length() > 0)
        selected = Math.min(profiles.length() - 1, getPreferences(0).getInt("selected", 0));
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
    root.setBackgroundColor(Ui.BG);
    root.setOnApplyWindowInsetsListener(
        (v, insets) -> {
          if (Build.VERSION.SDK_INT >= 30) {
            android.graphics.Insets bars =
                insets.getInsets(
                    WindowInsets.Type.systemBars()
                        | WindowInsets.Type.displayCutout()
                        | WindowInsets.Type.ime());
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom);
          } else
            v.setPadding(
                insets.getSystemWindowInsetLeft(),
                insets.getSystemWindowInsetTop(),
                insets.getSystemWindowInsetRight(),
                insets.getSystemWindowInsetBottom());
          return insets;
        });
    if (Build.VERSION.SDK_INT >= 30) getWindow().setDecorFitsSystemWindows(false);
    else
      getWindow()
          .getDecorView()
          .setSystemUiVisibility(
              getWindow().getDecorView().getSystemUiVisibility()
                  | View.SYSTEM_UI_FLAG_LAYOUT_STABLE
                  | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN);
    LinearLayout brand = new LinearLayout(this);
    brand.setGravity(Gravity.CENTER_VERTICAL);
    brand.setPadding(dp(24), dp(18), dp(24), dp(12));
    Ui.Icon mark = new Ui.Icon(this, 0);
    brand.addView(mark, new LinearLayout.LayoutParams(dp(26), dp(26)));
    TextView word = Ui.label(this, "MultiDNS", 22, Ui.INK, true);
    word.setPadding(dp(10), 0, 0, 0);
    brand.addView(word, new LinearLayout.LayoutParams(0, -2, 1));
    TextView version = Ui.label(this, BuildConfig.VERSION_NAME, 12, Ui.MUTED, false);
    version.setContentDescription("Version " + BuildConfig.VERSION_NAME);
    brand.addView(version);
    root.addView(brand);
    scroll = new ScrollView(this);
    scroll.setFillViewport(true);
    scroll.setClipToPadding(false);
    page = new LinearLayout(this);
    page.setOrientation(LinearLayout.VERTICAL);
    page.setPadding(dp(24), dp(12), dp(24), dp(24));
    scroll.addView(page);
    root.addView(scroll, new LinearLayout.LayoutParams(-1, 0, 1));
    LinearLayout nav = new LinearLayout(this);
    nav.setBackground(Ui.shape(this, Ui.SURFACE, 24, Ui.LINE));
    nav.setPadding(dp(8), dp(8), dp(8), dp(8));
    String[] names = {"Connect", "Scanner", "Routing", "Diagnostics"};
    for (int i = 0; i < names.length; i++) {
      final int tab = i;
      LinearLayout item = new LinearLayout(this);
      item.setOrientation(LinearLayout.VERTICAL);
      item.setGravity(Gravity.CENTER);
      item.setPadding(dp(4), dp(8), dp(4), dp(8));
      item.setBackground(Ui.ripple(this, Ui.SURFACE, 18, 0));
      Ui.Icon icon = new Ui.Icon(this, i);
      item.addView(icon, new LinearLayout.LayoutParams(dp(23), dp(23)));
      TextView label = Ui.label(this, names[i], 12, Ui.MUTED, true);
      label.setPadding(0, dp(5), 0, 0);
      item.addView(label);
      item.setContentDescription(names[i]);
      item.setFocusable(true);
      item.setOnClickListener(
          v -> {
            if (tab == 0) servers();
            else if (tab == 1) scanner();
            else if (tab == 2) routing();
            else diagnostics();
          });
      nav.addView(item, new LinearLayout.LayoutParams(0, -1, 1));
      navLabels.add(label);
      navIcons.add(icon);
      navItems.add(item);
    }
    LinearLayout.LayoutParams np = new LinearLayout.LayoutParams(-1, dp(76));
    np.setMargins(dp(16), 0, dp(16), dp(8));
    root.addView(nav, np);
    setContentView(root);
    root.requestApplyInsets();
  }

  private void selectTab(int tab) {
    currentTab = tab;
    for (int i = 0; i < navItems.size(); i++) {
      navItems.get(i).setBackground(Ui.ripple(this, i == tab ? Ui.SOFT : Ui.SURFACE, 18, 0));
      navItems.get(i).setSelected(i == tab);
      navLabels.get(i).setTextColor(i == tab ? Ui.ACCENT : Ui.MUTED);
      navIcons.get(i).color(i == tab ? Ui.ACCENT : Ui.MUTED);
    }
  }

  private void heading(String title, String subtitle) {
    TextView h = Ui.label(this, title, 30, Ui.INK, true);
    page.addView(h);
    TextView sub = text(page, subtitle);
    sub.setTextColor(Ui.MUTED);
    sub.setPadding(0, dp(8), 0, dp(18));
  }

  private LinearLayout card(LinearLayout parent) {
    LinearLayout box = new LinearLayout(this);
    box.setOrientation(LinearLayout.VERTICAL);
    box.setPadding(dp(20), dp(20), dp(20), dp(20));
    box.setBackground(Ui.shape(this, Ui.SURFACE, 22, Ui.LINE));
    LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-1, -2);
    p.setMargins(0, 0, 0, dp(16));
    parent.addView(box, p);
    return box;
  }

  private LinearLayout section(LinearLayout parent, String title, String subtitle) {
    LinearLayout box = card(parent);
    TextView name = Ui.label(this, title, 19, Ui.INK, true);
    box.addView(name);
    if (!subtitle.isEmpty()) text(box, subtitle).setTextColor(Ui.MUTED);
    return box;
  }

  private LinearLayout fold(LinearLayout parent, String title, String subtitle) {
    LinearLayout outer = card(parent);
    TextView toggle = Ui.label(this, title + "  +", 17, Ui.INK, true);
    toggle.setPadding(0, dp(8), 0, dp(8));
    toggle.setMinHeight(dp(48));
    toggle.setGravity(Gravity.CENTER_VERTICAL);
    outer.addView(toggle);
    LinearLayout content = new LinearLayout(this);
    content.setOrientation(LinearLayout.VERTICAL);
    content.setVisibility(View.GONE);
    outer.addView(content);
    if (!subtitle.isEmpty()) text(content, subtitle).setTextColor(Ui.MUTED);
    toggle.setOnClickListener(
        v -> {
          boolean open = content.getVisibility() != View.VISIBLE;
          content.setVisibility(open ? View.VISIBLE : View.GONE);
          toggle.setText(title + (open ? "  −" : "  +"));
          toggle.setContentDescription(title + (open ? ", expanded" : ", collapsed"));
        });
    return content;
  }

  private Spinner choices(LinearLayout parent, String label, String[] values, int selected) {
    if (!label.isEmpty()) text(parent, label).setTextColor(Ui.MUTED);
    Spinner spinner = new Spinner(this);
    spinner.setAdapter(
        new ArrayAdapter<>(this, android.R.layout.simple_spinner_dropdown_item, values));
    spinner.setSelection(Math.max(0, selected));
    Ui.styleSpinner(spinner);
    parent.addView(spinner);
    return spinner;
  }

  private void clear() {
    editing = false;
    diagnosticsSummary = null;
    diagnosticsPaths = null;
    resolverViews.clear();
    tuningOutput = null;
    applyTuning = null;
    page.removeAllViews();
    scroll.scrollTo(0, 0);
    dashboard = null;
    connectButton = null;
    downValue = null;
    upValue = null;
    healthValue = null;
    trafficValue = null;
    scanStatus = null;
    scanResults = null;
    latencyResults = null;
    statsAt = 0;
  }

  private void updateDashboard() {
    String state = TunnelService.state;
    boolean busy =
        TunnelService.instance != null
            || state.equals("Connecting")
            || state.equals("Disconnecting");
    dashboard.setText(state.startsWith("Connection failed") ? "Connection failed" : state);
    dashboard.setTextColor(state.equals("Connected") ? Ui.ACCENT : Ui.INK);
    if (connectButton != null) {
      connectButton.setText(
          state.equals("Disconnecting")
              ? "Disconnecting…"
              : busy
                  ? (state.equals("Connecting") ? "Cancel connection" : "Disconnect")
                  : "Connect");
      connectButton.setEnabled(!state.equals("Disconnecting") && (busy || profile() != null));
      Ui.styleButton(connectButton, !busy); // Keep one unambiguous connection control.
    }
    connectionSummary();
    if (state.startsWith("Connection failed") && trafficValue != null) trafficValue.setText(state);
  }

  private String connectionSummary() {
    try {
      JSONObject stats = new JSONObject(TunnelService.stats);
      if (!stats.has("queries")) {
        statsAt = 0;
        if (downValue != null) downValue.setText("0.00");
        if (upValue != null) upValue.setText("0.00");
        if (healthValue != null) healthValue.setText("Ready when you are");
        if (trafficValue != null) trafficValue.setText("Your connection activity appears here.");
        return "";
      }
      long now = SystemClock.elapsedRealtime();
      long up = stats.optLong("uploaded"), down = stats.optLong("downloaded");
      double uploadRate = 0, downloadRate = 0;
      if (statsAt > 0 && now > statsAt && up >= statsUploaded && down >= statsDownloaded) {
        uploadRate = (up - statsUploaded) * 0.008 / (now - statsAt);
        downloadRate = (down - statsDownloaded) * 0.008 / (now - statsAt);
      }
      statsAt = now;
      statsUploaded = up;
      statsDownloaded = down;
      JSONArray groups = stats.optJSONArray("groups");
      JSONArray resolvers = stats.optJSONArray("resolvers");
      int healthy = 0;
      for (int i = 0; resolvers != null && i < resolvers.length(); i++)
        if (resolvers.getJSONObject(i).optBoolean("healthy")) healthy++;
      if (downValue != null) downValue.setText(String.format(Locale.US, "%.2f", downloadRate));
      if (upValue != null) upValue.setText(String.format(Locale.US, "%.2f", uploadRate));
      if (healthValue != null)
        healthValue.setText(
            healthy
                + "/"
                + (resolvers == null ? 0 : resolvers.length())
                + " resolvers ready  ·  "
                + (groups == null ? 0 : groups.length())
                + " groups");
      if (trafficValue != null)
        trafficValue.setText(
            String.format(
                Locale.US,
                "↓ %.1f MB received   ·   ↑ %.1f MB sent\n"
                    + "%d active streams   ·   %d retries (%.1f%%)\n"
                    + "%d timeouts   ·   %d DNS errors",
                down / 1000000.0,
                up / 1000000.0,
                stats.optLong("streams"),
                stats.optLong("retries"),
                stats.optLong("retries") * 100.0 / Math.max(1, stats.optLong("exchanges")),
                stats.optLong("timeouts"),
                stats.optLong("dns_errors")));
      return "";
    } catch (JSONException e) {
      return "Waiting for connection statistics";
    }
  }

  private TextView text(LinearLayout parent, String content) {
    TextView t = Ui.label(this, content, 14, Ui.INK, false);
    t.setPadding(0, dp(8), 0, dp(8));
    parent.addView(t, new LinearLayout.LayoutParams(-1, -2));
    return t;
  }

  private Button button(LinearLayout parent, String title, Runnable action) {
    Button b = new Button(this);
    b.setText(title);
    Ui.styleButton(b, title.startsWith("Save") || title.equals("Start scan"));
    b.setOnClickListener(
        v -> {
          try {
            action.run();
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
    LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-1, -2);
    p.setMargins(0, dp(6), 0, dp(6));
    parent.addView(b, p);
    return b;
  }

  private EditText field(LinearLayout parent, String label, String value, boolean multiline) {
    TextView caption = text(parent, label);
    caption.setTextColor(Ui.MUTED);
    caption.setTextSize(13);
    EditText e = new EditText(this);
    e.setText(value);
    e.setSingleLine(!multiline);
    e.setInputType(
        InputType.TYPE_CLASS_TEXT
            | InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
            | (multiline ? InputType.TYPE_TEXT_FLAG_MULTI_LINE : 0));
    if (multiline) {
      e.setMinLines(3);
      e.setGravity(Gravity.TOP);
    }
    Ui.styleField(e);
    e.setContentDescription(label);
    LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-1, -2);
    p.setMargins(0, 0, 0, dp(8));
    parent.addView(e, p);
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
    selectTab(0);
    heading("Your connection", "A quieter path to the internet.");
    LinearLayout hero = card(page);
    TextView eyebrow = text(hero, "CONNECTION");
    eyebrow.setTextSize(11);
    eyebrow.setLetterSpacing(.12f);
    eyebrow.setTextColor(Ui.MUTED);
    dashboard = Ui.label(this, TunnelService.state, 23, Ui.INK, true);
    hero.addView(dashboard);
    if (profile() != null)
      text(hero, profile().optString("name") + "  ·  " + profile().optString("domain"))
          .setTextColor(Ui.MUTED);
    else text(hero, "Add a server to get started.").setTextColor(Ui.MUTED);
    connectButton =
        button(
            hero,
            "Connect",
            () -> {
              if (TunnelService.instance != null || TunnelService.state.equals("Connecting")) {
                TunnelService.disconnect(this);
              } else {
                if (profile() == null) return;
                if (tuningRunning) {
                  toast("Finish or cancel parameter discovery before connecting.");
                  return;
                }
                pendingProfile = profile().toString();
                Intent permission = VpnService.prepare(this);
                if (permission != null) startActivityForResult(permission, 41);
                else connect();
              }
              updateDashboard();
            });
    LinearLayout rates = new LinearLayout(this);
    LinearLayout left = new LinearLayout(this), right = new LinearLayout(this);
    left.setOrientation(LinearLayout.VERTICAL);
    right.setOrientation(LinearLayout.VERTICAL);
    text(left, "↓  Download").setTextColor(Ui.MUTED);
    text(right, "↑  Upload").setTextColor(Ui.MUTED);
    downValue = Ui.label(this, "0.00", 30, Ui.INK, true);
    upValue = Ui.label(this, "0.00", 30, Ui.INK, true);
    left.addView(downValue);
    right.addView(upValue);
    text(left, "Mbps").setTextColor(Ui.MUTED);
    text(right, "Mbps").setTextColor(Ui.MUTED);
    rates.addView(left, new LinearLayout.LayoutParams(0, -2, 1));
    rates.addView(right, new LinearLayout.LayoutParams(0, -2, 1));
    hero.addView(rates);
    healthValue = text(hero, "Ready when you are");
    healthValue.setTextColor(Ui.ACCENT);
    trafficValue = text(hero, "Your connection activity appears here.");
    trafficValue.setTextColor(Ui.MUTED);
    trafficValue.setTextSize(12);
    LinearLayout saved = section(page, "Servers", "Choose a profile for your next connection.");
    for (int i = 0; i < profiles.length(); i++) {
      final int index = i;
      JSONObject p = profiles.optJSONObject(i);
      LinearLayout row = new LinearLayout(this);
      row.setOrientation(LinearLayout.VERTICAL);
      row.setPadding(dp(16), dp(14), dp(16), dp(14));
      row.setBackground(
          Ui.ripple(this, i == selected ? Ui.SOFT : Ui.SURFACE, 14, i == selected ? 0 : Ui.LINE));
      TextView name =
          Ui.label(
              this,
              (i == selected ? "●  " : "○  ") + p.optString("name", "Server"),
              16,
              Ui.INK,
              true);
      row.addView(name);
      text(row, p.optString("domain")).setTextColor(Ui.MUTED);
      row.setOnClickListener(
          v -> {
            selected = index;
            getPreferences(0).edit().putInt("selected", selected).apply();
            servers();
          });
      row.setContentDescription(p.optString("name") + (i == selected ? ", selected" : ""));
      row.setFocusable(true);
      LinearLayout.LayoutParams rp = new LinearLayout.LayoutParams(-1, -2);
      rp.setMargins(0, dp(5), 0, dp(5));
      saved.addView(row, rp);
    }
    button(saved, "Add server", () -> edit(-1));
    button(
        saved,
        "Import profile",
        () ->
            startActivityForResult(
                new Intent(Intent.ACTION_OPEN_DOCUMENT)
                    .setType("application/json")
                    .addCategory(Intent.CATEGORY_OPENABLE),
                42));
    if (profile() != null) {
      button(saved, "Edit selected server", () -> edit(selected));
      Button more = button(saved, "Profile options", () -> {});
      more.setOnClickListener(
          v -> {
            PopupMenu menu = new PopupMenu(this, more);
            String[] labels = {
              "Test resolver latency",
              "Copy diagnostics",
              "Export without key",
              "Duplicate server",
              "Delete server"
            };
            for (int i = 0; i < labels.length; i++) menu.getMenu().add(0, i, i, labels[i]);
            menu.setOnMenuItemClickListener(
                item -> {
                  profileAction(item.getItemId());
                  return true;
                });
            menu.show();
          });
    }
    TextView privacy = text(page, "Profiles and server keys are encrypted on this device.");
    privacy.setTextColor(Ui.MUTED);
    privacy.setTextSize(12);
    updateDashboard();
  }

  private void profileAction(int action) {
    try {
      if (action == 0) {
        latencyTest();
        return;
      }
      if (action == 1) {
        getSystemService(ClipboardManager.class)
            .setPrimaryClip(ClipData.newPlainText("MultiDNS diagnostics", TunnelService.stats));
        Toast.makeText(this, "Diagnostics copied", Toast.LENGTH_SHORT).show();
        return;
      }
      if (action == 2) {
        JSONObject p = new JSONObject(profile().toString());
        p.remove("key");
        exportText = p.toString(2);
        exportingScan = false;
        createDocument("multidns-profile.json", "application/json", 43);
        return;
      }
      if (action == 3) {
        JSONObject p = new JSONObject(profile().toString());
        p.put("name", p.optString("name") + " copy");
        profiles.put(p);
        selected = profiles.length() - 1;
        save();
        servers();
        return;
      }
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
          .show();
    } catch (Exception e) {
      error(e.getMessage());
    }
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
    selectTab(0);
    editing = true;
    heading(index >= 0 ? "Edit server" : "Add server", "Your server, your paths.");
    LinearLayout identity = section(page, "Server", "Connection details");
    JSONObject original = index >= 0 ? profiles.optJSONObject(index) : empty();
    EditText name = field(identity, "Server name", original.optString("name"), false),
        domain = field(identity, "Delegated tunnel domain", original.optString("domain"), false),
        client = field(identity, "Client ID", original.optString("client_id", "phone"), false),
        key = field(identity, "Server authentication key", original.optString("key"), false);
    key.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
    LinearLayout paths =
        section(
            page,
            "Resolver groups",
            "Healthy resolvers share independent work. Failed paths are bypassed automatically.");
    text(paths, "One group per line. Separate members with commas. Up to 100 resolvers.")
        .setTextColor(Ui.MUTED);
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
    EditText members = field(paths, "Resolver groups", groups.toString(), true);
    LinearLayout groupTools =
        fold(
            paths, "Organize resolvers", "Regroup, keep spares, or drag resolvers between groups.");
    EditText size = field(groupTools, "Auto-group: resolvers per group", "2", false);
    size.setInputType(InputType.TYPE_CLASS_NUMBER);
    button(
        groupTools,
        "Regroup entered resolvers",
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
    LinearLayout tuning =
        fold(page, "Transport settings", "Automatic tuning keeps short requests responsive.");
    CheckBox adaptive = new CheckBox(this);
    adaptive.setText("Adapt paths: low RTT for short requests, throughput for bulk");
    adaptive.setChecked(original.optBoolean("adaptive", true));
    tuning.addView(adaptive);
    Ui.styleCheck(adaptive);
    CheckBox xor = new CheckBox(this);
    xor.setText("XOR obfuscation · zero added bytes (updated server required)");
    xor.setChecked(original.optString("obfuscation").equals("xor"));
    Ui.styleCheck(xor);
    tuning.addView(xor);
    text(
            tuning,
            "HTTPS already uses TLS. XOR masks payloads; it does not provide secure encryption."
                + " Authentication remains enabled.")
        .setTextColor(Ui.MUTED);
    EditText latencyBudget =
        field(
            tuning,
            "Target added network delay (5–500 ms)",
            "" + original.optInt("latency_target_ms", 30),
            false);
    latencyBudget.setInputType(InputType.TYPE_CLASS_NUMBER);
    EditText exchangeTimeout =
        field(
            tuning,
            "Maximum DNS timeout (100–30000 ms)",
            "" + original.optInt("timeout_ms", 1500),
            false);
    exchangeTimeout.setInputType(InputType.TYPE_CLASS_NUMBER);
    CheckBox automatic = new CheckBox(this);
    automatic.setText("Automatically form groups on each connection");
    automatic.setChecked(original.optBoolean("automatic_groups", false));
    tuning.addView(automatic);
    Ui.styleCheck(automatic);
    EditText
        groupSize =
            field(
                tuning,
                "Automatic: resolvers per group",
                String.valueOf(original.optInt("group_size", 2)),
                false),
        groupCount =
            field(
                tuning,
                "Automatic: maximum groups",
                String.valueOf(original.optInt("group_count", 5)),
                false);
    groupSize.setInputType(InputType.TYPE_CLASS_NUMBER);
    groupCount.setInputType(InputType.TYPE_CLASS_NUMBER);
    text(
        tuning,
        "Automatic mode tests all candidates concurrently before connecting. Excess healthy"
            + " resolvers stay as spares; a final group can contain fewer members. Health and group"
            + " membership are refreshed while connected.");
    EditText candidatePool =
        field(
            groupTools,
            "Additional candidate / spare resolvers, one per line",
            joinArray(original.optJSONArray("resolver_pool")),
            true);
    LinearLayout board = new LinearLayout(this);
    board.setOrientation(LinearLayout.VERTICAL);
    groupTools.addView(board);
    ArrayList<ArrayList<String>> boardGroups = parseGroupText(members.getText().toString());
    ArrayList<String> standby = new ArrayList<>();
    for (String token : candidatePool.getText().toString().split("[\\s,]+"))
      if (!token.trim().isEmpty()) standby.add(token);
    text(groupTools, "Hold a resolver to drag it into another group or the spare pool.");
    renderBoard(board, members, candidatePool, boardGroups, standby);
    button(
        groupTools,
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
        groupTools,
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
            new String[] {"Stripe", "Affinity"}));
    mode.setSelection(original.optString("mode").equals("affinity") ? 1 : 0);
    text(tuning, "Scheduling mode");
    Ui.styleSpinner(mode);
    tuning.addView(mode);
    EditText window =
        field(tuning, "Base request window (1–32)", "" + original.optInt("window", 8), false);
    window.setInputType(InputType.TYPE_CLASS_NUMBER);
    text(
            tuning,
            "Adaptive mode expands this window for sustained traffic while limiting each resolver"
                + " to its learned capacity. Start at 8.")
        .setTextColor(Ui.MUTED);
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
            p.put("mode", mode.getSelectedItemPosition() == 1 ? "affinity" : "stripe");
            p.put("window", Integer.parseInt(window.getText().toString()));
            p.put("adaptive", adaptive.isChecked());
            p.put("obfuscation", xor.isChecked() ? "xor" : "none");
            p.put("latency_target_ms", Integer.parseInt(latencyBudget.getText().toString()));
            p.put("timeout_ms", Integer.parseInt(exchangeTimeout.getText().toString()));
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
      box.setBackground(Ui.shape(this, index < 0 ? Ui.BG : Ui.SOFT, 14, 0));
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
        TextView card = text(box, resolver + measured + "  ⋮⋮");
        card.setBackground(Ui.ripple(this, Ui.SURFACE, 10, Ui.LINE));
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

  private void diagnostics() {
    clear();
    selectTab(3);
    heading("Diagnostics", "Understand each path. Measure before tuning.");
    LinearLayout summary =
        section(
            page,
            "Live transport",
            "Counters reset on connection. Byte totals count accepted payloads.");
    diagnosticsSummary = text(summary, "");
    diagnosticsSummary.setTextIsSelectable(true);
    button(summary, "Copy diagnostics", () -> copyDebug(TunnelService.stats));
    LinearLayout tune =
        section(
            page,
            "Parameter discovery",
            "Tests full-size authenticated TXT replies, upload payloads and TX/RX concurrency."
                + " Recommendations favor throughput within your latency budget; results vary with"
                + " network conditions.");
    button(tune, "Find optimal parameters", this::startParameterTest);
    button(
        tune,
        "Cancel parameter test",
        () -> {
          mobile.Engine e = tuningEngine;
          if (e != null) e.cancelParameterTest();
        });
    tuningOutput =
        text(
            tune,
            tuningRunning
                ? "Tests running…"
                : (tuningReport == null
                    ? "Ready. Update the server before testing."
                    : formatTuning(tuningReport)));
    tuningOutput.setTextIsSelectable(true);
    applyTuning = button(tune, "Apply found parameters to resolvers", this::applyParameters);
    applyTuning.setVisibility(canApplyTuning() ? View.VISIBLE : View.GONE);
    button(tune, "View parameter test details", this::showTuningDetails);
    button(
        tune,
        "Copy parameter test report",
        () -> {
          if (tuningReport != null) copyDebug(tuningReport);
        });
    if (profile() != null) {
      LinearLayout settings =
          section(
              page,
              "Settings",
              "Per-profile limits for adaptive mode. Reconnect to activate saved changes. TX/RX"
                  + " workers limit concurrent DNS exchanges across all streams; 0 = automatic.");
      button(settings, "Connection and transport settings", () -> edit(selected));
      JSONObject stored = profile().optJSONObject("resolver_settings");
      for (String endpoint : profileEndpoints()) {
        JSONObject cfg = stored == null ? null : stored.optJSONObject(endpoint);
        button(
            settings,
            endpoint
                + " · "
                + (cfg == null
                    ? "Auto"
                    : ("MTU "
                        + cfg.optInt("mtu", 1232)
                        + " · TX/RX "
                        + cfg.optInt("tx_workers")
                        + "/"
                        + cfg.optInt("rx_workers"))),
            () -> resolverSettings(endpoint));
      }
      button(
          settings,
          "Reset resolver limits to automatic",
          () -> {
            try {
              JSONObject copy = new JSONObject(profile().toString());
              copy.remove("resolver_settings");
              profiles.put(selected, copy);
              save();
              diagnostics();
              toast("Automatic limits saved. Reconnect to activate.");
            } catch (Exception e) {
              error(e.getMessage());
            }
          });
    }
    diagnosticsPaths =
        section(
            page,
            "Resolver statistics",
            "Queue delay includes local admission; RTT measures DNS exchanges. Retransmissions"
                + " count ARQ reissues on this path; backups are separate hedges.");
    updateDiagnostics();
  }

  private void toast(String message) {
    Toast.makeText(this, message, Toast.LENGTH_LONG).show();
  }

  private void copyDebug(String data) {
    ((ClipboardManager) getSystemService(CLIPBOARD_SERVICE))
        .setPrimaryClip(ClipData.newPlainText("MultiDNS diagnostics", data));
    toast("Copied");
  }

  private LinkedHashSet<String> profileEndpoints() {
    LinkedHashSet<String> out = new LinkedHashSet<>();
    if (profile() == null) return out;
    JSONArray groups = profile().optJSONArray("groups");
    for (int i = 0; groups != null && i < groups.length(); i++) {
      JSONArray rs = groups.optJSONObject(i).optJSONArray("resolvers");
      for (int j = 0; rs != null && j < rs.length(); j++) out.add(rs.optString(j));
    }
    JSONArray pool = profile().optJSONArray("resolver_pool");
    for (int i = 0; pool != null && i < pool.length(); i++) out.add(pool.optString(i));
    LinkedHashSet<String> normalized = new LinkedHashSet<>();
    for (String endpoint : out) {
      try {
        normalized.add(Mobile.normalizeResolver(endpoint));
      } catch (Exception ignored) {
      }
    }
    return normalized;
  }

  private void updateDiagnostics() {
    try {
      JSONObject stats = new JSONObject(TunnelService.stats);
      StringBuilder total =
          new StringBuilder(
              TunnelService.state
                  + " · "
                  + stats.optString("transport_version", BuildConfig.VERSION_NAME)
                  + "\n");
      String[] labels = {
        "Resolver attempts",
        "Replies",
        "ARQ retries",
        "Backup attempts",
        "Timeouts",
        "DNS errors",
        "Invalid replies",
        "Socket errors",
        "Not-ready replies",
        "Network changes",
        "Streams",
        "Queued requests"
      };
      String[] keys = {
        "queries",
        "replies",
        "retries",
        "hedges",
        "timeouts",
        "dns_errors",
        "invalid_replies",
        "socket_errors",
        "waits",
        "network_changes",
        "streams",
        "admission_queued"
      };
      for (int i = 0; i < keys.length; i++)
        total.append(labels[i]).append(": ").append(stats.optLong(keys[i])).append("\n");
      total.append(
          String.format(
              Locale.ROOT,
              "Oldest queued request: %.1f ms\n",
              stats.optDouble("oldest_admission_ms")));
      diagnosticsSummary.setText(total.toString());
      JSONArray resolvers = stats.optJSONArray("resolvers");
      for (int i = 0; resolvers != null && i < resolvers.length(); i++) {
        JSONObject r = resolvers.getJSONObject(i);
        String endpoint = r.getString("endpoint");
        TextView view = resolverViews.get(endpoint);
        if (view == null) {
          LinearLayout box =
              fold(diagnosticsPaths, endpoint, "Live counters and queue measurements");
          view = text(box, "");
          view.setTextIsSelectable(true);
          resolverViews.put(endpoint, view);
        }
        StringBuilder row =
            new StringBuilder(
                r.optBoolean("healthy") ? "Reachable" : "Unavailable · redistributing work");
        row.append(
            String.format(
                Locale.ROOT,
                "\nRTT %.1f ms · min %.1f · variance %.1f\nQueue %.1f ms · peak %.1f",
                r.optDouble("rtt_ms"),
                r.optDouble("min_rtt_ms"),
                r.optDouble("rtt_variance_ms"),
                r.optDouble("queue_ms"),
                r.optDouble("max_queue_ms")));
        String[] names = {
          "Capacity",
          "Capacity cuts from loss",
          "Capacity cuts from upload delay",
          "Idle capacity recoveries",
          "Wire in flight",
          "Active requests",
          "Attempts",
          "Replies",
          "Retransmissions",
          "Backups",
          "Timeouts",
          "DNS errors",
          "Invalid replies",
          "Socket errors",
          "Connections",
          "Reconnections",
          "Recoveries",
          "TCP fallbacks",
          "Uploaded bytes",
          "Downloaded bytes",
          "DNS MTU",
          "TX limit (0=auto)",
          "RX limit (0=auto)"
        };
        String[] fields = {
          "capacity",
          "capacity_loss_cuts",
          "capacity_delay_cuts",
          "capacity_recoveries",
          "wire_in_flight",
          "in_flight",
          "queries",
          "replies",
          "retransmissions",
          "hedges",
          "timeouts",
          "dns_errors",
          "invalid_replies",
          "socket_errors",
          "connections",
          "reconnections",
          "recoveries",
          "tcp_fallbacks",
          "uploaded",
          "downloaded",
          "mtu",
          "tx_workers",
          "rx_workers"
        };
        for (int j = 0; j < fields.length; j++)
          row.append("\n").append(names[j]).append(": ").append(r.optLong(fields[j]));
        row.append("\nLast error: ").append(r.optString("last_error", "none"));
        view.setText(row.toString());
      }
    } catch (Exception ignored) {
    }
  }

  private void resolverSettings(String endpoint) {
    if (profile() == null) return;
    JSONObject all = profile().optJSONObject("resolver_settings"),
        cfg = all == null ? null : all.optJSONObject(endpoint);
    if (cfg == null) cfg = new JSONObject();
    LinearLayout form = new LinearLayout(this);
    form.setOrientation(LinearLayout.VERTICAL);
    form.setPadding(dp(20), dp(8), dp(20), dp(12));
    text(
        form,
        "0 uses automatic limits. DNS MTU is the UDP DNS message budget. Upload payload is limited"
            + " by the tunnel domain. Workers cap in-flight exchanges.");
    EditText mtu = field(form, "DNS MTU (512–1232; 0 = auto)", "" + cfg.optInt("mtu"), false),
        tx = field(form, "TX workers (0–32)", "" + cfg.optInt("tx_workers"), false),
        rx = field(form, "RX workers (0–32)", "" + cfg.optInt("rx_workers"), false),
        upload =
            field(form, "Upload payload bytes (0 = auto)", "" + cfg.optInt("upload_mtu"), false);
    for (EditText input : new EditText[] {mtu, tx, rx, upload})
      input.setInputType(InputType.TYPE_CLASS_NUMBER);
    ScrollView scrollForm = new ScrollView(this);
    scrollForm.addView(form);
    AlertDialog dialog =
        new AlertDialog.Builder(this)
            .setTitle(endpoint)
            .setView(scrollForm)
            .setPositiveButton("Save", null)
            .setNegativeButton("Cancel", null)
            .create();
    dialog.setOnShowListener(
        v ->
            dialog
                .getButton(AlertDialog.BUTTON_POSITIVE)
                .setOnClickListener(
                    w -> {
                      try {
                        JSONObject copy = new JSONObject(profile().toString()),
                            limits = copy.optJSONObject("resolver_settings");
                        if (limits == null) limits = new JSONObject();
                        JSONObject values =
                            new JSONObject()
                                .put("mtu", Integer.parseInt(mtu.getText().toString()))
                                .put("tx_workers", Integer.parseInt(tx.getText().toString()))
                                .put("rx_workers", Integer.parseInt(rx.getText().toString()))
                                .put("upload_mtu", Integer.parseInt(upload.getText().toString()));
                        limits.put(endpoint, values);
                        copy.put("resolver_settings", limits);
                        Mobile.validate(copy.toString());
                        profiles.put(selected, copy);
                        save();
                        dialog.dismiss();
                        diagnostics();
                        toast("Saved. Reconnect to activate.");
                      } catch (Exception e) {
                        error(e.getMessage());
                      }
                    }));
    dialog.show();
  }

  private boolean canApplyTuning() {
    try {
      return !tuningRunning
          && tuningReport != null
          && tuningProfile != null
          && profile() != null
          && profile().toString().equals(tuningProfile)
          && !new JSONObject(tuningReport).optBoolean("cancelled")
          && new JSONObject(tuningReport).getJSONObject("resolver_settings").length() > 0;
    } catch (Exception e) {
      return false;
    }
  }

  private String formatTuning(String report) {
    try {
      JSONObject json = new JSONObject(report);
      StringBuilder out =
          new StringBuilder(
              json.optBoolean("cancelled")
                  ? "Cancelled. No settings applied.\n"
                  : "Completed. Review recommendations before applying.\n");
      JSONArray results = json.getJSONArray("resolvers");
      for (int i = 0; i < results.length(); i++) {
        JSONObject r = results.getJSONObject(i), cfg = r.getJSONObject("settings");
        out.append(r.getString("endpoint")).append("\n");
        if (r.has("error")) out.append(r.getString("error"));
        else
          out.append("DNS MTU ")
              .append(cfg.optInt("mtu"))
              .append(" · upload ")
              .append(cfg.optInt("upload_mtu"))
              .append(" B · TX ")
              .append(cfg.optInt("tx_workers"))
              .append(" / RX ")
              .append(cfg.optInt("rx_workers"))
              .append(" · ")
              .append(r.getJSONArray("trials").length())
              .append(" trials");
        out.append("\n");
      }
      return out.toString();
    } catch (Exception e) {
      return "Unable to read test report";
    }
  }

  private void showTuningDetails() {
    if (tuningReport == null) {
      toast("Run parameter discovery first.");
      return;
    }
    try {
      JSONObject report = new JSONObject(tuningReport);
      StringBuilder out = new StringBuilder(formatTuning(tuningReport));
      JSONArray results = report.getJSONArray("resolvers");
      for (int i = 0; i < results.length(); i++) {
        JSONObject resolver = results.getJSONObject(i);
        out.append("\n").append(resolver.getString("endpoint")).append("\n");
        JSONArray trials = resolver.getJSONArray("trials");
        for (int j = 0; j < trials.length(); j++) {
          JSONObject trial = trials.getJSONObject(j);
          out.append(
              String.format(
                  Locale.ROOT,
                  "%s · MTU %d · TX payload %d B · %d workers\n"
                      + "%d/%d replies · p95 %.1f ms · %.1f KiB/s\n\n",
                  trial.optString("direction").toUpperCase(Locale.ROOT),
                  trial.optInt("mtu"),
                  trial.optInt("upload_mtu"),
                  trial.optInt("workers"),
                  trial.optInt("received"),
                  trial.optInt("sent"),
                  trial.optDouble("p95_ms"),
                  trial.optDouble("bytes_per_second") / 1024));
        }
      }
      ScrollView scroller = new ScrollView(this);
      TextView detail = Ui.label(this, out.toString(), 14, Ui.INK, false);
      detail.setPadding(dp(20), dp(16), dp(20), dp(16));
      detail.setTextIsSelectable(true);
      scroller.addView(detail);
      new AlertDialog.Builder(this)
          .setTitle("Parameter test evidence")
          .setView(scroller)
          .setPositiveButton("Done", null)
          .show();
    } catch (Exception e) {
      error(e.getMessage());
    }
  }

  private void startParameterTest() {
    if (profile() == null) {
      error("Add a server first.");
      return;
    }
    if (tuningRunning) {
      toast("Tests are already running");
      return;
    }
    if (TunnelService.instance != null) {
      error(
          "Disconnect the VPN before parameter discovery so tests measure resolvers"
              + " independently.");
      return;
    }
    if (ScannerService.state.equals("Scanning")) {
      error("Stop the resolver scan before parameter discovery.");
      return;
    }
    tuningRunning = true;
    tuningReport = null;
    tuningProfile = profile().toString();
    tuningEngine = Mobile.newEngine();
    final String config = tuningProfile;
    final mobile.Engine engine = tuningEngine;
    if (applyTuning != null) applyTuning.setVisibility(View.GONE);
    new Thread(
            () -> {
              try {
                String report =
                    engine.findOptimalParameters(
                        config,
                        null,
                        message -> {
                          try {
                            JSONObject event = new JSONObject(message);
                            String line =
                                event.optString("Endpoint") + " · " + event.optString("Stage");
                            handler.post(
                                () -> {
                                  if (tuningOutput != null) tuningOutput.setText(line);
                                });
                          } catch (Exception ignored) {
                          }
                        });
                handler.post(
                    () -> {
                      tuningReport = report;
                      if (tuningOutput != null) tuningOutput.setText(formatTuning(report));
                      if (applyTuning != null)
                        applyTuning.setVisibility(canApplyTuning() ? View.VISIBLE : View.GONE);
                    });
              } catch (Exception e) {
                handler.post(() -> error(e.getMessage()));
              } finally {
                tuningRunning = false;
                tuningEngine = null;
                handler.post(
                    () -> {
                      if (applyTuning != null)
                        applyTuning.setVisibility(canApplyTuning() ? View.VISIBLE : View.GONE);
                    });
              }
            },
            "multidns-parameters")
        .start();
  }

  private void applyParameters() {
    if (!canApplyTuning()) {
      error("Run parameter discovery for the selected profile first.");
      return;
    }
    try {
      JSONObject report = new JSONObject(tuningReport),
          copy = new JSONObject(profile().toString()),
          settings = copy.optJSONObject("resolver_settings");
      if (settings == null) settings = new JSONObject();
      JSONObject found = report.getJSONObject("resolver_settings");
      Iterator<String> keys = found.keys();
      while (keys.hasNext()) {
        String endpoint = keys.next();
        settings.put(endpoint, found.getJSONObject(endpoint));
      }
      copy.put("resolver_settings", settings);
      copy.put("adaptive", true);
      Mobile.validate(copy.toString());
      profiles.put(selected, copy);
      save();
      tuningProfile = null;
      diagnostics();
      toast("Parameters saved for all working resolvers. Reconnect to activate.");
    } catch (Exception e) {
      error(e.getMessage());
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
    selectTab(0);
    editing = true;
    heading("Resolver latency", "Test every candidate through your tunnel server.");
    button(page, "Back to connection", this::servers);
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
    selectTab(2);
    heading("Routing", "Decide what takes the tunnel.");
    if (profile() == null) {
      text(page, "Add a server first.");
      return;
    }
    JSONObject p = profile();
    text(
            page,
            "App selection is applied first, then destination rules. Reconnect after saving"
                + " changes.")
        .setTextColor(Ui.MUTED);
    LinearLayout appCard =
        section(page, "Applications", "Choose all apps, an allowlist, or an exclusion list.");
    String[] modes = {"all", "include", "exclude"};
    Spinner appMode = new Spinner(this);
    appMode.setAdapter(
        new ArrayAdapter<>(
            this,
            android.R.layout.simple_spinner_dropdown_item,
            new String[] {"All apps", "Only selected apps", "Exclude selected apps"}));
    appMode.setSelection(Math.max(0, Arrays.asList(modes).indexOf(p.optString("app_mode", "all"))));
    Ui.styleSpinner(appMode);
    appCard.addView(appMode);
    final LinkedHashSet<String> apps = new LinkedHashSet<>();
    JSONArray saved = p.optJSONArray("apps");
    for (int i = 0; saved != null && i < saved.length(); i++) apps.add(saved.optString(i));
    TextView selectedApps =
        text(appCard, apps.isEmpty() ? "No apps selected" : apps.size() + " apps selected");
    button(
        appCard,
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
          final LinkedHashSet<String> draft = new LinkedHashSet<>(apps);
          new AlertDialog.Builder(this)
              .setTitle("Apps to include or exclude")
              .setMultiChoiceItems(
                  labels,
                  checked,
                  (d, which, on) -> {
                    if (on) draft.add(ids[which]);
                    else draft.remove(ids[which]);
                  })
              .setPositiveButton(
                  "Done",
                  (d, w) -> {
                    apps.clear();
                    apps.addAll(draft);
                    selectedApps.setText(apps.size() + " apps selected");
                  })
              .setNegativeButton("Cancel", null)
              .show();
        });
    LinearLayout advancedApps =
        fold(appCard, "Additional apps", "For applications without a launcher icon.");
    EditText appIds =
        field(
            advancedApps,
            "Additional package IDs, one per line (for apps without launcher icons)",
            "",
            true);
    LinearLayout rulesCard =
        section(page, "Destination rules", "First match wins. Reconnect to apply your changes.");
    String[] actions = {"tunnel", "direct", "block"};
    Spinner fallback = new Spinner(this);
    fallback.setAdapter(
        new ArrayAdapter<>(this, android.R.layout.simple_spinner_dropdown_item, actions));
    fallback.setSelection(Arrays.asList(actions).indexOf(p.optString("default_action", "tunnel")));
    text(rulesCard, "Default action");
    Ui.styleSpinner(fallback);
    rulesCard.addView(fallback);
    ArrayList<JSONObject> rules = new ArrayList<>();
    JSONArray savedRules = p.optJSONArray("rules");
    for (int i = 0; savedRules != null && i < savedRules.length(); i++)
      rules.add(savedRules.optJSONObject(i));
    LinearLayout ruleList = new LinearLayout(this);
    ruleList.setOrientation(LinearLayout.VERTICAL);
    rulesCard.addView(ruleList);
    renderRules(ruleList, rules);
    button(rulesCard, "Add rule", () -> editRule(ruleList, rules, -1));
    text(rulesCard, "Domain rules use observed DNS. Encrypted external DNS can hide names.")
        .setTextColor(Ui.MUTED);
    button(
        page,
        "Save routing",
        () -> {
          try {
            JSONObject copy = new JSONObject(p.toString());
            copy.put("app_mode", modes[appMode.getSelectedItemPosition()]);
            for (String id : appIds.getText().toString().split("\\s+"))
              if (!id.trim().isEmpty()) apps.add(id.trim());
            copy.put("apps", new JSONArray(apps));
            copy.put("default_action", fallback.getSelectedItem().toString());
            copy.put("rules", new JSONArray(rules));
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

  private void renderRules(LinearLayout list, ArrayList<JSONObject> rules) {
    list.removeAllViews();
    if (rules.isEmpty())
      text(list, "No rules yet. All traffic uses the default action.").setTextColor(Ui.MUTED);
    for (int i = 0; i < rules.size(); i++) {
      final int index = i;
      JSONObject rule = rules.get(i);
      LinearLayout row = new LinearLayout(this);
      row.setGravity(Gravity.CENTER_VERTICAL);
      row.setPadding(dp(12), dp(8), dp(4), dp(8));
      row.setBackground(Ui.shape(this, Ui.BG, 14, 0));
      LinearLayout info = new LinearLayout(this);
      info.setOrientation(LinearLayout.VERTICAL);
      String destination = rule.optString("domain", "");
      if (destination.isEmpty()) destination = rule.optString("cidr", "");
      if (destination.isEmpty()) destination = "All destinations";
      TextView name = Ui.label(this, (i + 1) + ".  " + destination, 15, Ui.INK, true);
      info.addView(name);
      text(
              info,
              rule.optString("action", "tunnel")
                  + " · "
                  + rule.optString("protocol", "any protocol")
                  + (rule.optInt("port", 0) > 0 ? " · port " + rule.optInt("port") : ""))
          .setTextColor(Ui.MUTED);
      row.addView(info, new LinearLayout.LayoutParams(0, -2, 1));
      info.setOnClickListener(v -> editRule(list, rules, index));
      Button more = new Button(this);
      more.setText("⋮");
      more.setContentDescription("Options for rule " + (i + 1));
      Ui.styleButton(more, false);
      row.addView(more, new LinearLayout.LayoutParams(dp(48), dp(48)));
      more.setOnClickListener(
          v -> {
            PopupMenu menu = new PopupMenu(this, more);
            menu.getMenu().add(0, 0, 0, "Edit");
            menu.getMenu().add(0, 1, 1, "Move up").setEnabled(index > 0);
            menu.getMenu().add(0, 2, 2, "Move down").setEnabled(index < rules.size() - 1);
            menu.getMenu().add(0, 3, 3, "Delete");
            menu.setOnMenuItemClickListener(
                item -> {
                  int action = item.getItemId();
                  if (action == 0) editRule(list, rules, index);
                  else {
                    if (action == 1) Collections.swap(rules, index, index - 1);
                    else if (action == 2) Collections.swap(rules, index, index + 1);
                    else rules.remove(index);
                    renderRules(list, rules);
                  }
                  return true;
                });
            menu.show();
          });
      LinearLayout.LayoutParams rp = new LinearLayout.LayoutParams(-1, -2);
      rp.setMargins(0, dp(5), 0, dp(5));
      list.addView(row, rp);
    }
  }

  private void editRule(LinearLayout list, ArrayList<JSONObject> rules, int index) {
    JSONObject rule = index < 0 ? new JSONObject() : rules.get(index);
    LinearLayout form = new LinearLayout(this);
    form.setOrientation(LinearLayout.VERTICAL);
    form.setPadding(dp(20), dp(4), dp(20), dp(12));
    EditText domain = field(form, "Domain (optional)", rule.optString("domain"), false);
    EditText cidr = field(form, "IP network / CIDR (optional)", rule.optString("cidr"), false);
    EditText port =
        field(
            form,
            "Port (optional)",
            rule.optInt("port") == 0 ? "" : "" + rule.optInt("port"),
            false);
    port.setInputType(InputType.TYPE_CLASS_NUMBER);
    String[] protocols = {"any", "tcp", "udp"}, actions = {"tunnel", "direct", "block"};
    Spinner protocol =
        choices(
            form,
            "Protocol",
            protocols,
            Arrays.asList(protocols).indexOf(rule.optString("protocol", "any")));
    Spinner action =
        choices(
            form,
            "Action",
            actions,
            Arrays.asList(actions).indexOf(rule.optString("action", "tunnel")));
    CheckBox exact = new CheckBox(this);
    exact.setText("Match this domain only");
    exact.setChecked(rule.optBoolean("exact"));
    Ui.styleCheck(exact);
    form.addView(exact);
    ScrollView scroller = new ScrollView(this);
    scroller.addView(form);
    AlertDialog dialog =
        new AlertDialog.Builder(this)
            .setTitle(index < 0 ? "Add rule" : "Edit rule")
            .setView(scroller)
            .setPositiveButton("Done", null)
            .setNegativeButton("Cancel", null)
            .create();
    dialog.setOnShowListener(
        d ->
            dialog
                .getButton(AlertDialog.BUTTON_POSITIVE)
                .setOnClickListener(
                    v -> {
                      try {
                        JSONObject updated = new JSONObject(rule.toString());
                        updated.remove("domain");
                        updated.remove("cidr");
                        updated.remove("port");
                        updated.remove("protocol");
                        if (!domain.getText().toString().trim().isEmpty())
                          updated.put("domain", domain.getText().toString().trim());
                        if (!cidr.getText().toString().trim().isEmpty())
                          updated.put("cidr", cidr.getText().toString().trim());
                        if (!port.getText().toString().trim().isEmpty())
                          updated.put("port", Integer.parseInt(port.getText().toString().trim()));
                        if (protocol.getSelectedItemPosition() > 0)
                          updated.put("protocol", protocol.getSelectedItem().toString());
                        updated.put("action", action.getSelectedItem().toString());
                        updated.put("exact", exact.isChecked());
                        JSONObject validation = new JSONObject(profile().toString());
                        validation.put("rules", new JSONArray().put(updated));
                        Mobile.validate(validation.toString());
                        if (index < 0) rules.add(updated);
                        else rules.set(index, updated);
                        renderRules(list, rules);
                        dialog.dismiss();
                      } catch (Exception e) {
                        error(e.getMessage());
                      }
                    }));
    dialog.show();
  }

  private void scanner() {
    clear();
    selectTab(1);
    heading("Find your paths", "Discover resolvers that carry your tunnel.");
    if (profile() == null) {
      text(page, "Add a server profile so the scanner can verify the actual tunnel.");
      return;
    }
    text(page, "Results are saved as they arrive. Pause and resume without losing your place.")
        .setTextColor(Ui.MUTED);
    LinearLayout settings = section(page, "Scan settings", "Targets and limits");
    android.content.SharedPreferences prefs = getSharedPreferences("scanner", 0);
    ranges =
        field(
            settings,
            "IPs, CIDRs, or start–end ranges",
            prefs.getString("ranges", "1.1.1.1\n8.8.8.8"),
            true);
    LinearLayout scanAdvanced =
        fold(settings, "Scan options", "For IPv6, use narrow ranges. Scans bypass this VPN.");
    workers = field(scanAdvanced, "DNS workers (1–256)", prefs.getString("workers", "16"), false);
    timeout =
        field(
            scanAdvanced,
            "Timeout per test, milliseconds",
            prefs.getString("timeout", "1500"),
            false);
    zone = field(scanAdvanced, "NS probe zone (optional)", prefs.getString("zone", ""), false);
    workers.setInputType(InputType.TYPE_CLASS_NUMBER);
    timeout.setInputType(InputType.TYPE_CLASS_NUMBER);
    LinearLayout results =
        section(page, "Live results", "Only authenticated tunnel matches are included.");
    txtFilter = new CheckBox(this);
    txtFilter.setText("Only TXT verified");
    txtFilter.setChecked(true);
    results.addView(txtFilter);
    Ui.styleCheck(txtFilter);
    nsFilter = new CheckBox(this);
    nsFilter.setText("Also require NS → A");
    results.addView(nsFilter);
    Ui.styleCheck(nsFilter);
    scanStatus = text(results, ScannerService.state);
    scanResults = text(results, "");
    scanResults.setTextIsSelectable(true);
    scanResults.setTypeface(android.graphics.Typeface.MONOSPACE);
    scanResults.setTextSize(12);
    page.removeView(results);
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
    page.addView(results);
    LinearLayout exports =
        fold(page, "Use these results", "Copy, export, or add matching resolvers to your server.");
    button(
        exports,
        "Copy matching resolvers",
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
        exports,
        "Export resolver list",
        () -> {
          exportingScan = true;
          createDocument("multidns-resolvers.txt", "text/plain", 43);
        });
    button(
        exports,
        "Add results to server",
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
  public void onBackPressed() {
    if (editing || currentTab != 0) {
      servers();
      return;
    }
    super.onBackPressed();
  }

  @Override
  protected void onDestroy() {
    if (tuningEngine != null) tuningEngine.cancelParameterTest();
    handler.removeCallbacks(refresh);
    super.onDestroy();
  }
}
