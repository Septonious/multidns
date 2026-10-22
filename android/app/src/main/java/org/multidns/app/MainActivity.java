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
  private Button connectButton, scanControl;
  private ImageButton resumeScan;
  private final ArrayList<TextView> navLabels = new ArrayList<>();
  private LinearLayout rateSection;
  private final ArrayList<LinearLayout> navItems = new ArrayList<>();
  private int currentTab;
  private LinearLayout resolverContainer;
  private final LinkedHashMap<String, ResolverCard> resolverCards = new LinkedHashMap<>();
  private final LinkedHashMap<String, JSONObject> latencyRows = new LinkedHashMap<>();
  private LatencyTester latencyTester;
  private Boolean resolverLive;
  private Button latencyButton, cancelLatency, debugCopy;
  private final ArrayList<ImageView> navIcons = new ArrayList<>();

  private boolean editing;
  private TextView dashboard, scanStatus, scanResults;
  private EditText ranges, workers, timeout, zone;
  private CheckBox txtFilter, nsFilter;
  private final Handler handler = new Handler(Looper.getMainLooper());
  private String pendingProfile, exportText;
  private boolean exportingScan;
  private boolean latencyRunning;
  private boolean scanRefreshing;
  private long statsAt, statsUploaded, statsDownloaded;
  private final Runnable refresh =
      new Runnable() {
        public void run() {
          if (dashboard != null) updateDashboard();
          if (scanControl != null) updateScanControl();
          if (resolverContainer != null) updateResolverCards();
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
      for (int i = 0; i < profiles.length(); i++) {
        profiles.getJSONObject(i).remove("resolver_settings");
        profiles.getJSONObject(i).put("adaptive", true);
      }
      if (profiles.length() > 0)
        selected = Math.min(profiles.length() - 1, getPreferences(0).getInt("selected", 0));
    } catch (Exception e) {
      error("Unable to decrypt saved profiles: " + e.getMessage());
    }
    layout();
    servers();
    if (Build.VERSION.SDK_INT >= 33)
      requestPermissions(new String[] {Manifest.permission.POST_NOTIFICATIONS}, 99);
  }

  private int dp(int n) {
    return (int) (n * getResources().getDisplayMetrics().density);
  }

  private void layout() {
    root = new LinearLayout(this);
    root.setOrientation(LinearLayout.VERTICAL);
    root.setBackgroundColor(Ui.BG);
    root.setFocusableInTouchMode(true);
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
    brand.setPadding(dp(20), dp(16), dp(20), dp(8));
    TextView word = Ui.label(this, "MultiDNS", 22, Ui.INK, true);

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
    page.setPadding(dp(20), dp(12), dp(20), dp(20));
    scroll.addView(page);
    root.addView(scroll, new LinearLayout.LayoutParams(-1, 0, 1));
    LinearLayout nav = new LinearLayout(this);
    nav.setBackground(Ui.shape(this, Ui.SURFACE, 24, Ui.LINE));
    nav.setPadding(dp(4), dp(4), dp(4), dp(4));
    String[] names = {"Servers", "Scan", "Routing", "Resolvers"};
    int[] icons = {
      R.drawable.ic_server, R.drawable.ic_scan, R.drawable.ic_route, R.drawable.ic_dns
    };
    for (int i = 0; i < names.length; i++) {
      final int tab = i;
      LinearLayout item = new LinearLayout(this);
      item.setOrientation(LinearLayout.VERTICAL);
      item.setGravity(Gravity.CENTER);
      item.setPadding(dp(4), 0, dp(4), 0);
      item.setMinimumHeight(dp(48));
      item.setBackground(Ui.ripple(this, Ui.SURFACE, 18, 0));
      TextView label = Ui.label(this, names[i], 10, Ui.MUTED, true);
      label.setMaxLines(1);
      label.setEllipsize(android.text.TextUtils.TruncateAt.END);
      ImageView icon = new ImageView(this);
      icon.setImageResource(icons[i]);
      icon.setImageTintList(android.content.res.ColorStateList.valueOf(Ui.MUTED));
      LinearLayout.LayoutParams ip = new LinearLayout.LayoutParams(dp(20), dp(20));
      ip.bottomMargin = dp(4);
      item.addView(icon, ip);
      navIcons.add(icon);
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

      navItems.add(item);
    }
    LinearLayout.LayoutParams np = new LinearLayout.LayoutParams(-1, dp(64));
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
      navIcons
          .get(i)
          .setImageTintList(
              android.content.res.ColorStateList.valueOf(i == tab ? Ui.ACCENT : Ui.MUTED));
    }
  }

  private void heading(String title, String subtitle) {
    TextView h = Ui.label(this, title, 26, Ui.INK, true);
    h.setPadding(0, 0, 0, dp(16));
    page.addView(h);
    if (!subtitle.isEmpty()) {
      TextView sub = text(page, subtitle);
      sub.setTextColor(Ui.MUTED);
      sub.setPadding(0, 0, 0, dp(16));
    }
  }

  private LinearLayout card(LinearLayout parent) {
    LinearLayout box = new LinearLayout(this);
    box.setOrientation(LinearLayout.VERTICAL);
    box.setPadding(dp(16), dp(16), dp(16), dp(16));
    box.setBackground(Ui.shape(this, Ui.SURFACE, 16, Ui.LINE));
    LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-1, -2);
    p.setMargins(0, 0, 0, dp(12));
    parent.addView(box, p);
    return box;
  }

  private LinearLayout section(LinearLayout parent, String title, String subtitle) {
    LinearLayout box = card(parent);
    TextView name = Ui.label(this, title, 17, Ui.INK, true);
    name.setPadding(0, 0, 0, dp(8));
    box.addView(name);
    if (!subtitle.isEmpty()) text(box, subtitle).setTextColor(Ui.MUTED);
    return box;
  }

  private LinearLayout fold(LinearLayout parent, String title, String subtitle) {
    LinearLayout outer;
    if (parent == page) {
      outer = card(parent);
    } else {
      outer = new LinearLayout(this);
      outer.setOrientation(LinearLayout.VERTICAL);
      LinearLayout.LayoutParams margins = new LinearLayout.LayoutParams(-1, -2);
      margins.setMargins(0, dp(4), 0, dp(4));
      parent.addView(outer, margins);
    }
    TextView toggle = Ui.label(this, title, 16, Ui.INK, true);
    toggle.setPadding(0, dp(8), 0, dp(8));
    toggle.setCompoundDrawablesRelativeWithIntrinsicBounds(0, 0, R.drawable.ic_expand_more, 0);
    toggle.setCompoundDrawablePadding(dp(12));
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
          toggle.setCompoundDrawablesRelativeWithIntrinsicBounds(
              0, 0, open ? R.drawable.ic_expand_less : R.drawable.ic_expand_more, 0);
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
    root.requestFocus();
    ((android.view.inputmethod.InputMethodManager) getSystemService(INPUT_METHOD_SERVICE))
        .hideSoftInputFromWindow(root.getWindowToken(), 0);
    scanControl = null;
    resumeScan = null;
    editing = false;
    resolverContainer = null;
    resolverCards.clear();
    resolverLive = null;
    latencyButton = null;
    cancelLatency = null;
    debugCopy = null;
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
    rateSection = null;
    statsAt = 0;
  }

  private void updateDashboard() {
    String state = TunnelService.state;
    boolean busy =
        TunnelService.instance != null
            || state.equals("Connecting")
            || state.equals("Disconnecting");
    dashboard.setText(state.startsWith("Connection failed") ? "Connection failed" : state);
    dashboard.setTextColor(
        state.equals("Connected")
            ? Ui.GREEN
            : state.startsWith("Connection failed")
                ? Ui.RED
                : state.equals("Connecting") ? Ui.YELLOW : Ui.MUTED);
    if (connectButton != null) {
      connectButton.setText(
          state.equals("Disconnecting")
              ? "Disconnecting…"
              : busy
                  ? (state.equals("Connecting") ? "Cancel connection" : "Disconnect")
                  : "Connect");
      connectButton.setEnabled(!state.equals("Disconnecting") && (busy || profile() != null));
      Ui.styleButton(connectButton, !busy);
      Ui.icon(
          connectButton,
          busy ? R.drawable.ic_stop : R.drawable.ic_power,
          busy ? Ui.RED : Ui.ON_ACCENT); // Keep one unambiguous connection control.
    }
    connectionSummary();
    if (state.startsWith("Connection failed") && trafficValue != null) trafficValue.setText(state);
  }

  private String connectionSummary() {
    try {
      JSONObject stats = new JSONObject(TunnelService.stats);
      if (!stats.has("uploaded")) {
        statsAt = 0;
        if (downValue != null) downValue.setText("0.00");
        if (upValue != null) upValue.setText("0.00");
        if (healthValue != null) healthValue.setVisibility(View.GONE);
        if (rateSection != null) rateSection.setVisibility(View.GONE);
        if (trafficValue != null) trafficValue.setVisibility(View.GONE);
        return "";
      }
      if (rateSection != null) rateSection.setVisibility(View.VISIBLE);
      if (healthValue != null) healthValue.setVisibility(View.VISIBLE);
      if (trafficValue != null) trafficValue.setVisibility(View.VISIBLE);
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
      for (int i = 0; resolvers != null && i < resolvers.length(); i++) {
        JSONObject resolver = resolvers.getJSONObject(i);
        if (resolver.optBoolean(
            "reachable",
            !resolver.optString("state").isEmpty()
                && !resolver.optString("state").equals("red")
                && !resolver.optString("status").equals("Checking"))) healthy++;
      }
      if (downValue != null) downValue.setText(String.format(Locale.US, "%.2f", downloadRate));
      if (upValue != null) upValue.setText(String.format(Locale.US, "%.2f", uploadRate));
      if (healthValue != null)
        healthValue.setText(
            healthy
                + "/"
                + (resolvers == null ? 0 : resolvers.length())
                + " resolvers reachable  ·  "
                + stats.optInt("group_count", 0)
                + " groups");
      if (trafficValue != null)
        trafficValue.setText(
            String.format(
                Locale.US, "%.1f MB received  ·  %.1f MB sent", down / 1000000.0, up / 1000000.0));
      return "";
    } catch (JSONException e) {
      return "Waiting for connection statistics";
    }
  }

  private TextView text(LinearLayout parent, String content) {
    TextView t = Ui.label(this, content, 14, Ui.INK, false);
    t.setPadding(0, dp(4), 0, dp(4));
    parent.addView(t, new LinearLayout.LayoutParams(-1, -2));
    return t;
  }

  private Button button(LinearLayout parent, String title, Runnable action) {
    Button b = new Button(this);
    b.setText(title);
    boolean primary = title.startsWith("Save") || title.equals("Start scan");
    Ui.styleButton(b, primary);
    int icon =
        title.startsWith("Save")
            ? R.drawable.ic_check
            : title.startsWith("Copy")
                ? R.drawable.ic_copy
                : title.startsWith("Add")
                    ? R.drawable.ic_plus
                    : title.contains("scan")
                        ? R.drawable.ic_scan
                        : title.startsWith("Test") ? R.drawable.ic_pulse : 0;
    if (icon != 0) Ui.icon(b, icon, primary ? Ui.ON_ACCENT : Ui.ACCENT);
    b.setOnClickListener(
        v -> {
          try {
            action.run();
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
    LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-1, -2);
    p.setMargins(0, dp(4), 0, dp(4));
    parent.addView(b, p);
    return b;
  }

  private EditText field(LinearLayout parent, String label, String value, boolean multiline) {
    TextView caption = text(parent, label);
    caption.setTextColor(Ui.MUTED);
    caption.setTextSize(13);
    caption.setPadding(0, dp(8), 0, dp(6));
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
      getPreferences(0).edit().putInt("selected", selected).apply();
    } catch (Exception e) {
      throw new IllegalStateException(e);
    }
  }

  private boolean connected() {
    return TunnelService.instance != null;
  }

  private ImageButton iconButton(
      LinearLayout parent, int resource, String description, Runnable action) {
    ImageButton button = new ImageButton(this);
    button.setImageResource(resource);
    button.setImageTintList(android.content.res.ColorStateList.valueOf(Ui.ACCENT));
    button.setBackground(Ui.ripple(this, Ui.SOFT, 12, 0));
    button.setPadding(dp(12), dp(12), dp(12), dp(12));
    button.setContentDescription(description);
    button.setOnClickListener(
        v -> {
          try {
            action.run();
          } catch (Exception e) {
            error(e.getMessage());
          }
        });
    LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(dp(48), dp(48));
    lp.leftMargin = dp(8);
    parent.addView(button, lp);
    return button;
  }

  private void importProfile() {
    if (connected()) {
      toast("Disconnect before importing a server");
      return;
    }
    startActivityForResult(
        new Intent(Intent.ACTION_OPEN_DOCUMENT)
            .setType("application/json")
            .addCategory(Intent.CATEGORY_OPENABLE),
        42);
  }

  private void servers() {
    clear();
    selectTab(0);
    LinearLayout hero = card(page);
    dashboard = Ui.label(this, TunnelService.state, 18, Ui.INK, true);
    hero.addView(dashboard);
    if (profile() != null) {
      TextView server = text(hero, profile().optString("name"));
      server.setTextSize(25);
      server.setTypeface(
          android.graphics.Typeface.create("sans-serif-medium", android.graphics.Typeface.NORMAL));
      text(hero, profile().optString("domain")).setTextColor(Ui.MUTED);
    }
    LinearLayout rates = new LinearLayout(this);
    rateSection = rates;
    rates.setPadding(0, dp(16), 0, dp(16));
    LinearLayout left = new LinearLayout(this), right = new LinearLayout(this);
    left.setOrientation(LinearLayout.VERTICAL);
    right.setOrientation(LinearLayout.VERTICAL);
    TextView dl = text(left, "Download"), ul = text(right, "Upload");
    Ui.icon(dl, R.drawable.ic_download, Ui.ACCENT);
    Ui.icon(ul, R.drawable.ic_upload, Ui.PURPLE);
    downValue = Ui.label(this, "0.00", 30, Ui.ACCENT, true);
    upValue = Ui.label(this, "0.00", 30, Ui.PURPLE, true);
    left.addView(downValue);
    right.addView(upValue);
    text(left, "Mbps").setTextColor(Ui.MUTED);
    text(right, "Mbps").setTextColor(Ui.MUTED);
    rates.addView(left, new LinearLayout.LayoutParams(0, -2, 1));
    rates.addView(right, new LinearLayout.LayoutParams(0, -2, 1));
    hero.addView(rates);
    connectButton =
        button(
            hero,
            "Connect",
            () -> {
              if (connected() || TunnelService.state.equals("Connecting"))
                TunnelService.disconnect(this);
              else {
                if (profile() == null) return;
                stopLatencyTest();
                try {
                  JSONObject config = new JSONObject(profile().toString());
                  config.remove("resolver_settings");
                  config.put("adaptive", true);
                  pendingProfile = config.toString();
                } catch (JSONException e) {
                  throw new IllegalStateException(e);
                }
                Intent permission = VpnService.prepare(this);
                if (permission != null) startActivityForResult(permission, 41);
                else connect();
              }
              updateDashboard();
            });
    healthValue = text(hero, "");
    healthValue.setTextColor(Ui.GREEN);
    trafficValue = text(hero, "");
    trafficValue.setTextColor(Ui.MUTED);
    trafficValue.setTextSize(12);
    LinearLayout tools = new LinearLayout(this);
    tools.setGravity(Gravity.CENTER_VERTICAL);
    tools.setPadding(0, dp(8), 0, dp(12));
    tools.addView(
        Ui.label(this, "Servers", 20, Ui.INK, true), new LinearLayout.LayoutParams(0, -2, 1));
    iconButton(tools, R.drawable.ic_import, "Import server profile", this::importProfile);
    iconButton(tools, R.drawable.ic_plus, "Add server", () -> edit(-1));
    page.addView(tools);
    if (profiles.length() == 0) text(page, "Add or import your server.").setTextColor(Ui.MUTED);
    for (int i = 0; i < profiles.length(); i++) {
      final int index = i;
      JSONObject config = profiles.optJSONObject(i);
      LinearLayout row = new LinearLayout(this);
      row.setGravity(Gravity.CENTER_VERTICAL);
      row.setPadding(dp(12), dp(10), dp(8), dp(10));
      row.setBackground(
          Ui.ripple(this, i == selected ? Ui.SOFT : Ui.SURFACE, 14, i == selected ? Ui.ACCENT : 0));
      ImageView icon = new ImageView(this);
      icon.setImageResource(i == selected ? R.drawable.ic_check : R.drawable.ic_server);
      icon.setImageTintList(
          android.content.res.ColorStateList.valueOf(i == selected ? Ui.ACCENT : Ui.MUTED));
      row.addView(icon, new LinearLayout.LayoutParams(dp(24), dp(24)));
      LinearLayout identity = new LinearLayout(this);
      identity.setOrientation(LinearLayout.VERTICAL);
      identity.setPadding(dp(12), 0, dp(4), 0);
      TextView name = Ui.label(this, config.optString("name", "Server"), 16, Ui.INK, true);
      name.setMaxLines(1);
      name.setEllipsize(android.text.TextUtils.TruncateAt.END);
      identity.addView(name);
      TextView domain = text(identity, config.optString("domain"));
      domain.setTextColor(Ui.MUTED);
      domain.setTextSize(12);
      domain.setMaxLines(1);
      domain.setEllipsize(android.text.TextUtils.TruncateAt.END);
      row.addView(identity, new LinearLayout.LayoutParams(0, -2, 1));
      ImageButton more =
          iconButton(row, R.drawable.ic_more, "Options for " + config.optString("name"), () -> {});
      more.setBackground(Ui.ripple(this, 0x00000000, 12, 0));
      more.setOnClickListener(v -> serverMenu(more, index));
      row.setOnClickListener(
          v -> {
            if (connected()) {
              toast("Disconnect before switching servers");
              return;
            }
            selected = index;
            getPreferences(0).edit().putInt("selected", selected).apply();
            servers();
          });
      row.setOnLongClickListener(
          v -> {
            serverMenu(more, index);
            return true;
          });
      row.setFocusable(true);
      row.setContentDescription(config.optString("name") + (i == selected ? ", selected" : ""));
      LinearLayout.LayoutParams rp = new LinearLayout.LayoutParams(-1, -2);
      rp.bottomMargin = dp(8);
      page.addView(row, rp);
    }
    updateDashboard();
  }

  private void serverMenu(View anchor, int index) {
    PopupMenu menu = new PopupMenu(this, anchor);
    String[] labels = {"Edit", "Export without key", "Duplicate", "Delete"};
    for (int i = 0; i < labels.length; i++) menu.getMenu().add(0, i, i, labels[i]);
    menu.setOnMenuItemClickListener(
        item -> {
          try {
            if (connected() && item.getItemId() != 1) {
              toast("Disconnect before changing servers");
              return true;
            }
            JSONObject config = new JSONObject(profiles.getJSONObject(index).toString());
            if (item.getItemId() == 0) edit(index);
            else if (item.getItemId() == 1) {
              config.remove("key");
              exportText = config.toString(2);
              exportingScan = false;
              createDocument("multidns-profile.json", "application/json", 43);
            } else if (item.getItemId() == 2) {
              config.put("name", config.optString("name") + " copy");
              profiles.put(config);
              selected = profiles.length() - 1;
              save();
              servers();
            } else
              new AlertDialog.Builder(this)
                  .setTitle("Delete server?")
                  .setMessage(config.optString("name"))
                  .setNegativeButton("Cancel", null)
                  .setPositiveButton(
                      "Delete",
                      (d, w) -> {
                        profiles.remove(index);
                        if (profiles.length() == 0) selected = -1;
                        else if (index < selected) selected--;
                        else if (index == selected)
                          selected = Math.min(index, profiles.length() - 1);
                        save();
                        servers();
                      })
                  .show();
          } catch (Exception e) {
            error(e.getMessage());
          }
          return true;
        });
    menu.show();
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
    if (connected()) {
      toast("Disconnect before editing servers");
      return;
    }
    clear();
    selectTab(0);
    editing = true;
    heading(index >= 0 ? "Edit server" : "Add server", "");
    LinearLayout identity = section(page, "Server", "");
    JSONObject original = index >= 0 ? profiles.optJSONObject(index) : empty();
    EditText name = field(identity, "Server name", original.optString("name"), false),
        domain = field(identity, "Delegated tunnel domain", original.optString("domain"), false),
        client = field(identity, "Client ID", original.optString("client_id", "phone"), false),
        key = field(identity, "Server authentication key", original.optString("key"), false);
    key.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
    LinearLayout paths =
        section(page, "Resolver groups", "Active groups share capacity. Spares provide failover.");
    text(paths, "One group per line; separate members with commas.").setTextColor(Ui.MUTED);
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
    LinearLayout tuning = fold(page, "Transport settings", "");
    text(tuning, "Transport adapts automatically to each resolver.").setTextColor(Ui.ACCENT);
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
    EditText loadedLimit =
        field(
            tuning,
            "Loaded RTT budget (100–2000 ms)",
            "" + original.optInt("loaded_rtt_limit_ms", 500),
            false);
    loadedLimit.setInputType(InputType.TYPE_CLASS_NUMBER);
    text(tuning, "Adaptive target, not a guarantee during path failures.").setTextColor(Ui.MUTED);
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
            p.put("window", 8);
            p.put("adaptive", true);
            p.remove("resolver_settings");
            p.put("obfuscation", xor.isChecked() ? "xor" : "none");
            p.put("loaded_rtt_limit_ms", Integer.parseInt(loadedLimit.getText().toString()));
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
                    ? String.format(java.util.Locale.ROOT, " · %.1f ms", h.optDouble("rtt_ms", 0))
                    : " · not working";
        } catch (Exception ignored) {
        }
        TextView card = text(box, resolver + measured);
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

  private void toast(String message) {
    Toast.makeText(this, message, Toast.LENGTH_SHORT).show();
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
    for (String endpoint : out)
      try {
        normalized.add(Mobile.normalizeResolver(endpoint));
      } catch (Exception ignored) {
      }
    return normalized;
  }

  private final class ResolverCard {
    final LinearLayout box;
    final TextView ip, latency, detail, status;
    int lastColor;

    ResolverCard(String endpoint) {
      box = new LinearLayout(MainActivity.this);
      box.setOrientation(LinearLayout.VERTICAL);
      box.setPadding(dp(12), dp(10), dp(12), dp(10));
      LinearLayout top = new LinearLayout(MainActivity.this);
      top.setGravity(Gravity.CENTER_VERTICAL);
      ip = Ui.label(MainActivity.this, endpoint.replaceFirst(":53$", ""), 15, Ui.INK, true);
      ip.setMaxLines(1);
      ip.setEllipsize(android.text.TextUtils.TruncateAt.END);
      top.addView(ip, new LinearLayout.LayoutParams(0, -2, 1));
      latency = Ui.label(MainActivity.this, "—", 15, Ui.MUTED, true);
      latency.setPadding(dp(8), 0, 0, 0);
      top.addView(latency);
      box.addView(top);
      LinearLayout bottom = new LinearLayout(MainActivity.this);
      bottom.setPadding(0, dp(7), 0, 0);
      bottom.setGravity(Gravity.CENTER_VERTICAL);
      detail = Ui.label(MainActivity.this, "", 12, Ui.MUTED, false);
      detail.setMaxLines(2);
      bottom.addView(detail, new LinearLayout.LayoutParams(0, -2, 1));
      status = Ui.label(MainActivity.this, "", 11, Ui.MUTED, true);
      status.setPadding(dp(8), dp(4), dp(8), dp(4));
      bottom.addView(status);
      box.addView(bottom);
      LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-1, -2);
      lp.bottomMargin = dp(7);
      resolverContainer.addView(box, lp);
    }

    void set(String state, String label, String timing, String sub) {
      int color = state.equals("green") ? Ui.GREEN : state.equals("red") ? Ui.RED : Ui.YELLOW;
      if (lastColor != color) {
        box.setBackground(Ui.shape(MainActivity.this, Ui.SURFACE, 12, color));
        status.setBackground(Ui.shape(MainActivity.this, Ui.tint(color), 8, 0));
        status.setTextColor(color);
        latency.setTextColor(color);
        lastColor = color;
      }
      if (!latency.getText().toString().equals(timing)) latency.setText(timing);
      if (!detail.getText().toString().equals(sub)) detail.setText(sub);
      if (!status.getText().toString().equals(label)) status.setText(label);
      box.setContentDescription(ip.getText() + ", " + timing + ", " + sub + ", " + label);
    }
  }

  private void diagnostics() {
    clear();
    selectTab(3);
    heading("Resolvers", "");
    resolverLive = connected();
    if (!resolverLive) {
      LinearLayout actions = new LinearLayout(this);
      actions.setGravity(Gravity.CENTER_VERTICAL);
      TextView label = Ui.label(this, "ICMP + end-to-end", 14, Ui.MUTED, false);
      actions.addView(label, new LinearLayout.LayoutParams(0, -2, 1));
      latencyButton =
          button(actions, latencyRunning ? "Testing…" : "Test latency", this::latencyTest);
      // Buttons in a horizontal toolbar must wrap instead of taking the full width.
      latencyButton.setLayoutParams(new LinearLayout.LayoutParams(-2, -2));
      latencyButton.setEnabled(!latencyRunning && profile() != null);
      cancelLatency =
          button(
              actions,
              "Cancel",
              () -> {
                stopLatencyTest();
                diagnostics();
              });
      cancelLatency.setLayoutParams(new LinearLayout.LayoutParams(-2, -2));
      cancelLatency.setVisibility(latencyRunning ? View.VISIBLE : View.GONE);
      page.addView(actions);
    } else {
      TextView live = text(page, "LIVE  ·  Automatic transport");
      live.setTextColor(Ui.GREEN);
      live.setPadding(0, 0, 0, dp(12));
    }
    resolverContainer = new LinearLayout(this);
    resolverContainer.setOrientation(LinearLayout.VERTICAL);
    page.addView(resolverContainer);
    if (!resolverLive)
      for (String endpoint : profileEndpoints()) {
        JSONObject result = latencyRows.get(endpoint);
        if (result == null && !latencyRunning)
          try {
            result = new JSONObject(getSharedPreferences("latency", 0).getString(endpoint, "{}"));
          } catch (Exception ignored) {
          }
        renderLatency(endpoint, result);
      }
    LinearLayout debug = card(page);
    LinearLayout toggleRow = new LinearLayout(this);
    toggleRow.setGravity(Gravity.CENTER_VERTICAL);
    toggleRow.addView(
        Ui.label(this, "Debug capture", 15, Ui.INK, true), new LinearLayout.LayoutParams(0, -2, 1));
    Switch toggle = new Switch(this);
    toggle.setContentDescription("Capture detailed diagnostics");
    toggle.setChecked(getSharedPreferences("ui", 0).getBoolean("debug", false));
    toggleRow.addView(toggle);
    debug.addView(toggleRow);
    text(debug, "Detailed transport data. Off by default.").setTextColor(Ui.MUTED);
    debugCopy = button(debug, "Copy debug report", () -> copyDebug(TunnelService.debugStats));
    debugCopy.setEnabled(toggle.isChecked() && !TunnelService.debugStats.equals("{}"));
    toggle.setOnCheckedChangeListener(
        (v, on) -> {
          TunnelService.diagnostics(this, on);
          debugCopy.setEnabled(on && !TunnelService.debugStats.equals("{}"));
        });
    updateResolverCards();
  }

  private void updateResolverCards() {
    if (resolverContainer == null) return;
    if (resolverLive == null || resolverLive != connected()) {
      diagnostics();
      return;
    }
    if (debugCopy != null)
      debugCopy.setEnabled(
          getSharedPreferences("ui", 0).getBoolean("debug", false)
              && !TunnelService.debugStats.equals("{}"));
    if (!resolverLive) return;
    try {
      JSONArray rows = new JSONObject(TunnelService.stats).optJSONArray("resolvers");
      for (int i = 0; rows != null && i < rows.length(); i++) {
        JSONObject row = rows.getJSONObject(i);
        String endpoint = row.getString("endpoint");
        ResolverCard card = resolverCards.get(endpoint);
        if (card == null) {
          card = new ResolverCard(endpoint);
          resolverCards.put(endpoint, card);
        }
        double rtt = row.optDouble("rtt_ms", 0);
        card.set(
            row.optString("state", "yellow"),
            row.optString("status", "Checking"),
            rtt > 0 ? String.format(Locale.ROOT, "%.0f ms", rtt) : "—",
            row.optInt("streams")
                + (row.optInt("streams") == 1 ? " stream" : " streams")
                + "  ·  "
                + row.optString("transport", "udp").toUpperCase(Locale.ROOT));
      }
    } catch (JSONException ignored) {
    }
  }

  private void renderLatency(String endpoint, JSONObject row) {
    if (resolverContainer == null || Boolean.TRUE.equals(resolverLive)) return;
    ResolverCard card = resolverCards.get(endpoint);
    if (card == null) {
      card = new ResolverCard(endpoint);
      resolverCards.put(endpoint, card);
    }
    boolean tested = row != null && row.has("healthy"), ok = tested && row.optBoolean("healthy");
    double e2e = row == null ? 0 : row.optDouble("rtt_ms", 0),
        icmp = row == null ? -1 : row.optDouble("icmp_ms", -1);
    String sub =
        "ICMP "
            + (icmp >= 0
                ? String.format(Locale.ROOT, "%.0f ms", icmp)
                : row == null ? "—" : row.optString("icmp_status", "—"))
            + "  ·  E2E "
            + (ok ? String.format(Locale.ROOT, "%.0f ms", e2e) : tested ? "No reply" : "—");
    card.set(
        !tested ? "yellow" : ok ? "green" : "red",
        !tested ? (latencyRunning ? "Testing…" : "Not tested") : ok ? "Reachable" : "Unavailable",
        ok ? String.format(Locale.ROOT, "%.0f ms", e2e) : "—",
        sub);
  }

  private void stopLatencyTest() {
    if (latencyTester != null) {
      latencyTester.stop();
      latencyTester = null;
    }
    latencyRunning = false;
  }

  private void latencyTest() {
    if (ScannerService.running) {
      toast("Pause scanning before testing latency");
      return;
    }
    if (connected() || profile() == null || latencyRunning) return;
    latencyRows.clear();
    latencyRunning = true;
    LatencyTester tester = new LatencyTester();
    latencyTester = tester;
    final String config = profile().toString();
    diagnostics();
    tester.run(
        config,
        new LatencyTester.Events() {
          public void row(String endpoint, JSONObject result) {
            handler.post(
                () -> {
                  if (latencyTester != tester) return;
                  latencyRows.put(endpoint, result);
                  getSharedPreferences("latency", 0)
                      .edit()
                      .putString(endpoint, result.toString())
                      .apply();
                  if (currentTab == 3) renderLatency(endpoint, result);
                });
          }

          public void failed(String message) {
            handler.post(() -> toast(message == null ? "Latency test failed" : message));
          }

          public void finished() {
            handler.post(
                () -> {
                  if (latencyTester != tester) return;
                  latencyRunning = false;
                  latencyTester = null;
                  if (latencyButton != null) {
                    latencyButton.setText("Test again");
                    latencyButton.setEnabled(true);
                  }
                  if (cancelLatency != null) cancelLatency.setVisibility(View.GONE);
                });
          }
        });
  }

  private void routing() {
    clear();
    selectTab(2);
    heading("Routing", "");
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
    LinearLayout rulesCard = section(page, "Destination rules", "First match wins.");
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
    if (rules.isEmpty()) text(list, "No rules. The default action applies.").setTextColor(Ui.MUTED);
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
      ImageButton more =
          iconButton(row, R.drawable.ic_more, "Options for rule " + (i + 1), () -> {});
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

  private void updateScanControl() {
    boolean running = ScannerService.running;
    scanControl.setText(running ? "Pause scan" : "Start scan");
    Ui.styleButton(scanControl, !running);
    Ui.icon(
        scanControl,
        running ? R.drawable.ic_stop : R.drawable.ic_scan,
        running ? Ui.YELLOW : Ui.ON_ACCENT);
    resumeScan.setEnabled(
        !running
            && !ScannerService.state.equals("Completed")
            && getSharedPreferences("scan-progress", 0).getLong("next", 0) > 0);
    resumeScan.setAlpha(resumeScan.isEnabled() ? 1f : .35f);
    if (scanStatus != null)
      scanStatus.setTextColor(
          running ? Ui.ACCENT : ScannerService.state.equals("Completed") ? Ui.GREEN : Ui.MUTED);
  }

  private void startScan(boolean resume) {
    stopLatencyTest();
    try {
      if (ScannerService.running) return;
      getSharedPreferences("scanner", 0)
          .edit()
          .putString("ranges", ranges.getText().toString())
          .putString("workers", workers.getText().toString())
          .putString("timeout", timeout.getText().toString())
          .putString("zone", zone.getText().toString())
          .apply();
      JSONObject options =
          new JSONObject()
              .put("ranges", ranges.getText().toString())
              .put("workers", Integer.parseInt(workers.getText().toString()))
              .put("timeout_ms", Integer.parseInt(timeout.getText().toString()))
              .put("ns_zone", zone.getText().toString().trim())
              .put("profile", profile());
      startManagedService(
          new Intent(this, ScannerService.class)
              .putExtra("options", options.toString())
              .putExtra("resume", resume));
    } catch (Exception e) {
      error(e.getMessage());
    }
  }

  private void scanner() {
    clear();
    selectTab(1);
    heading("Scanner", "");
    if (profile() == null) {
      text(page, "Add a server profile so the scanner can verify the actual tunnel.");
      return;
    }

    LinearLayout settings = section(page, "Scan settings", "");
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
    LinearLayout results = section(page, "Results", "");
    scanStatus = text(results, ScannerService.state);
    LinearLayout filters = fold(results, "Filters", "");
    txtFilter = new CheckBox(this);
    txtFilter.setText("Only TXT verified");
    txtFilter.setChecked(true);
    filters.addView(txtFilter);
    Ui.styleCheck(txtFilter);
    nsFilter = new CheckBox(this);
    nsFilter.setText("Also require NS → A");
    filters.addView(nsFilter);
    Ui.styleCheck(nsFilter);
    scanResults = text(results, "");
    scanResults.setTextIsSelectable(true);
    scanResults.setTypeface(android.graphics.Typeface.MONOSPACE);
    scanResults.setTextSize(12);
    page.removeView(results);
    LinearLayout actions = new LinearLayout(this);
    actions.setGravity(Gravity.CENTER_VERTICAL);
    scanControl =
        button(
            actions,
            "Start scan",
            () -> {
              if (ScannerService.running)
                startService(new Intent(this, ScannerService.class).setAction("STOP"));
              else startScan(false);
            });
    scanControl.setLayoutParams(new LinearLayout.LayoutParams(0, -2, 1));
    resumeScan =
        iconButton(actions, R.drawable.ic_resume, "Resume paused scan", () -> startScan(true));
    page.addView(actions);
    updateScanControl();
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
        if (connected()) {
          toast("Disconnect before importing a server");
          return;
        }
        try (InputStream in = getContentResolver().openInputStream(data.getData())) {
          ByteArrayOutputStream b = new ByteArrayOutputStream();
          byte[] chunk = new byte[8192];
          int n;
          while ((n = in.read(chunk)) != -1) {
            if (b.size() + n > 1024 * 1024) throw new Exception("Profile is too large");
            b.write(chunk, 0, n);
          }
          JSONObject p = new JSONObject(b.toString(StandardCharsets.UTF_8.name()));
          p.remove("resolver_settings");
          p.put("adaptive", true);
          p.put("window", 8);
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
  protected void onResume() {
    super.onResume();
    TunnelService.refreshVisible();
    handler.removeCallbacks(refresh);
    handler.post(refresh);
  }

  @Override
  protected void onPause() {
    TunnelService.uiVisible = false;
    handler.removeCallbacks(refresh);
    super.onPause();
  }

  @Override
  protected void onDestroy() {
    stopLatencyTest();
    handler.removeCallbacks(refresh);
    super.onDestroy();
  }
}
