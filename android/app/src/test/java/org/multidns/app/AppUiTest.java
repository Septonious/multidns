package org.multidns.app;

import static org.junit.Assert.*;

import android.graphics.Bitmap;
import android.graphics.Canvas;
import android.view.View;
import android.view.ViewGroup;
import android.widget.TextView;
import java.io.File;
import java.io.FileOutputStream;
import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.time.Duration;
import mobile.Mobile;
import org.json.*;
import org.junit.Test;
import org.junit.runner.RunWith;
import org.robolectric.Robolectric;
import org.robolectric.RobolectricTestRunner;
import org.robolectric.RuntimeEnvironment;
import org.robolectric.annotation.Config;
import org.robolectric.annotation.GraphicsMode;
import org.robolectric.annotation.Implementation;
import org.robolectric.annotation.Implements;
import org.robolectric.shadows.ShadowSystemClock;

@RunWith(RobolectricTestRunner.class)
@Config(
    sdk = 35,
    shadows = {AppUiTest.MobileShadow.class},
    instrumentedPackages = {"mobile"})
@GraphicsMode(GraphicsMode.Mode.NATIVE)
public class AppUiTest {
  @Implements(value = Mobile.class, isInAndroidSdk = false)
  public static class MobileShadow {
    @Implementation
    protected static void __staticInitializer__() {}

    @Implementation
    protected static String normalizeResolver(String endpoint) {
      return endpoint.contains(":") ? endpoint : endpoint + ":53";
    }
  }

  static void set(Object object, String name, Object value) throws Exception {
    Field f = object.getClass().getDeclaredField(name);
    f.setAccessible(true);
    f.set(object, value);
  }

  static Object get(Object object, String name) throws Exception {
    Field f = object.getClass().getDeclaredField(name);
    f.setAccessible(true);
    return f.get(object);
  }

  static void page(MainActivity activity, String name) throws Exception {
    Method m = MainActivity.class.getDeclaredMethod(name);
    m.setAccessible(true);
    m.invoke(activity);
  }

  static JSONObject profile() throws Exception {
    JSONArray groups = new JSONArray();
    for (int i = 0; i < 4; i++)
      groups.put(
          new JSONObject()
              .put("name", "Group " + (i + 1))
              .put(
                  "resolvers",
                  new JSONArray()
                      .put("192.0.2." + (i * 2 + 1) + ":53")
                      .put("192.0.2." + (i * 2 + 2) + ":53")));
    return new JSONObject()
        .put("name", "Atmoslink")
        .put("domain", "t.example.test")
        .put("key", "ui-fixture-key-not-production")
        .put("groups", groups)
        .put("rules", new JSONArray())
        .put("default_action", "tunnel")
        .put("adaptive", true)
        .put("window", 8);
  }

  static void render(MainActivity activity, String name, int width, int height) throws Exception {
    View root = (View) get(activity, "root");
    root.measure(
        View.MeasureSpec.makeMeasureSpec(width, View.MeasureSpec.EXACTLY),
        View.MeasureSpec.makeMeasureSpec(height, View.MeasureSpec.EXACTLY));
    root.layout(0, 0, width, height);
    Bitmap bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888);
    root.draw(new Canvas(bitmap));
    File folder = new File(System.getProperty("multidns.screenshots", "build/reports/ui"));
    folder.mkdirs();
    try (FileOutputStream out = new FileOutputStream(new File(folder, name + ".png"))) {
      bitmap.compress(Bitmap.CompressFormat.PNG, 100, out);
    }
    bitmap.recycle();
    assertEquals(width, root.getWidth());
    assertTrue(
        "fixed header must remain visible",
        ((ViewGroup) root).getChildAt(0).getTop() >= root.getScrollY());
    for (Object item : (java.util.List<?>) get(activity, "navItems")) {
      View view = (View) item;
      assertTrue("navigation touch target", view.getHeight() >= 48);
    }
    for (Object label : (java.util.List<?>) get(activity, "navLabels")) {
      TextView view = (TextView) label;
      assertNotNull(view.getLayout());
      assertEquals(
          "navigation label clipped: " + view.getText(), 0, view.getLayout().getEllipsisCount(0));
    }
  }

  @Test
  public void screensAt360dp() throws Exception {
    RuntimeEnvironment.setQualifiers("w360dp-h800dp-mdpi");
    var controller = Robolectric.buildActivity(MainActivity.class).setup();
    MainActivity activity = controller.get();
    set(activity, "profiles", new JSONArray().put(profile()));
    set(activity, "selected", 0);
    for (String name : new String[] {"servers", "scanner", "routing", "diagnostics"}) {
      page(activity, name);
      render(activity, name, 360, 800);
    }
    Method edit = MainActivity.class.getDeclaredMethod("edit", int.class);
    edit.setAccessible(true);
    edit.invoke(activity, 0);
    render(activity, "editor", 360, 800);
    TunnelService.state = "Connected";
    TunnelService.instance = new TunnelService();
    JSONArray live = new JSONArray();
    for (int i = 0; i < 8; i++)
      live.put(
          new JSONObject()
              .put("endpoint", "192.0.2." + (i + 1) + ":53")
              .put("state", i >= 4 && i <= 6 ? "yellow" : i == 7 ? "red" : "green")
              .put("reachable", i != 7)
              .put(
                  "status",
                  i == 4
                      ? "High latency"
                      : i == 5
                          ? "Download limited"
                          : i == 6 ? "Recent loss" : i == 7 ? "Unavailable" : "Active")
              .put("streams", i == 6 ? 12 : i == 7 ? 0 : 3)
              .put("transport", i == 0 ? "tcp" : "udp")
              .put("rtt_ms", 43 + i * 4));
    TunnelService.stats =
        new JSONObject()
            .put("transport_version", "0.3.8")
            .put("uploaded", 2200000)
            .put("downloaded", 6960000)
            .put("streams", 8)
            .put("group_count", 4)
            .put("resolvers", live)
            .toString();
    ShadowSystemClock.advanceBy(Duration.ofSeconds(2));
    page(activity, "servers");
    set(activity, "statsAt", android.os.SystemClock.elapsedRealtime() - 1000);
    set(activity, "statsUploaded", 2120000L);
    set(activity, "statsDownloaded", 6520000L);
    pageSummary(activity);
    assertTrue(
        ((TextView) get(activity, "healthValue"))
            .getText()
            .toString()
            .startsWith("7/8 resolvers reachable"));
    render(activity, "connected", 360, 800);
    page(activity, "diagnostics");
    render(activity, "resolvers-live", 360, 800);
    java.util.Map<?, ?> rows = (java.util.Map<?, ?>) get(activity, "resolverCards");
    assertEquals(8, rows.size());
    for (Object card : rows.values())
      assertTrue(((TextView) get(card, "detail")).getText().toString().contains("streams"));
    assertTrue(
        ((TextView) get(rows.get("192.0.2.1:53"), "detail")).getText().toString().contains("TCP"));
    assertTrue(
        ((TextView) get(rows.get("192.0.2.2:53"), "detail")).getText().toString().contains("UDP"));
    assertNull(
        "manual parameter discovery must be absent",
        findText((View) get(activity, "root"), "Find optimal parameters"));
    android.widget.Switch capture = findSwitch((View) get(activity, "root"));
    assertNotNull(capture);
    capture.setChecked(false);
    android.widget.Button copy = (android.widget.Button) get(activity, "debugCopy");
    assertFalse(copy.isEnabled());
    capture.setChecked(true);
    TunnelService.debugStats = "{\"queries\":17}";
    page(activity, "updateResolverCards");
    assertTrue(copy.isEnabled());
    copy.performClick();
    assertEquals(
        TunnelService.debugStats,
        activity
            .getSystemService(android.content.ClipboardManager.class)
            .getPrimaryClip()
            .getItemAt(0)
            .getText()
            .toString());
    capture.setChecked(false);
    assertEquals("{}", TunnelService.debugStats);
    assertFalse(copy.isEnabled());
    TunnelService.instance = null;
    TunnelService.state = "Disconnected";
    TunnelService.stats = "{}";
    page(activity, "updateResolverCards");
    assertNotNull(get(activity, "latencyButton"));
    controller.pause().stop().destroy();
  }

  static TextView findText(View root, String text) {
    if (root instanceof TextView && ((TextView) root).getText().toString().equals(text))
      return (TextView) root;
    if (root instanceof ViewGroup)
      for (int i = 0; i < ((ViewGroup) root).getChildCount(); i++) {
        TextView match = findText(((ViewGroup) root).getChildAt(i), text);
        if (match != null) return match;
      }
    return null;
  }

  static android.widget.Switch findSwitch(View root) {
    if (root instanceof android.widget.Switch) return (android.widget.Switch) root;
    if (root instanceof ViewGroup)
      for (int i = 0; i < ((ViewGroup) root).getChildCount(); i++) {
        android.widget.Switch match = findSwitch(((ViewGroup) root).getChildAt(i));
        if (match != null) return match;
      }
    return null;
  }

  static void pageSummary(MainActivity activity) throws Exception {
    Method m = MainActivity.class.getDeclaredMethod("connectionSummary");
    m.setAccessible(true);
    m.invoke(activity);
  }

  @Test
  public void narrowScreenLargeFont() throws Exception {
    RuntimeEnvironment.setQualifiers("w320dp-h720dp-mdpi");
    RuntimeEnvironment.setFontScale(1.3f);
    var controller = Robolectric.buildActivity(MainActivity.class).setup();
    MainActivity activity = controller.get();
    set(activity, "profiles", new JSONArray().put(profile()));
    set(activity, "selected", 0);
    page(activity, "servers");
    render(activity, "narrow-font-130", 320, 720);
    page(activity, "diagnostics");
    render(activity, "diagnostics-narrow-font-130", 320, 720);
    TunnelService.state = "Connected";
    TunnelService.instance = new TunnelService();
    JSONArray live = new JSONArray();
    String[] reasons = {"Recent loss", "Download limited", "High latency"};
    for (int i = 0; i < reasons.length; i++)
      live.put(
          new JSONObject()
              .put("endpoint", "192.0.2." + (i + 1) + ":53")
              .put("state", "yellow")
              .put("status", reasons[i])
              .put("streams", 12)
              .put("rtt_ms", 44));
    TunnelService.stats = new JSONObject().put("resolvers", live).toString();
    page(activity, "diagnostics");
    render(activity, "resolver-reasons-narrow-font-130", 320, 720);
    java.util.Map<?, ?> rows = (java.util.Map<?, ?>) get(activity, "resolverCards");
    for (int i = 0; i < reasons.length; i++) {
      Object card = rows.get("192.0.2." + (i + 1) + ":53");
      TextView status = (TextView) get(card, "status");
      assertEquals(reasons[i], status.getText().toString());
      assertEquals("status clipped", 0, status.getLayout().getEllipsisCount(0));
      assertTrue(
          "reason extends outside card",
          status.getRight() <= ((View) status.getParent()).getWidth());
    }
    TunnelService.instance = null;
    TunnelService.state = "Disconnected";
    TunnelService.stats = "{}";
    controller.pause().stop().destroy();
    RuntimeEnvironment.setFontScale(1f);
  }
}
