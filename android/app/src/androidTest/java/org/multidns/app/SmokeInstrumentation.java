package org.multidns.app;

import android.app.*;
import android.content.*;
import android.graphics.Bitmap;
import android.net.*;
import android.os.*;
import android.view.*;
import android.widget.*;
import java.io.*;
import org.json.*;

/** Device-only lifecycle regression. Uses an isolated test server/profile. */
public final class SmokeInstrumentation extends Instrumentation {
  private MainActivity activity;
  private Bundle options;

  @Override
  public void onCreate(Bundle args) {
    super.onCreate(args);
    options = args;
    start();
  }

  @Override
  public void onStart() {
    Bundle result = new Bundle();
    try {
      Context context = getTargetContext();
      JSONObject profile =
          new JSONObject()
              .put("name", "Atmoslink")
              .put("domain", "t.test")
              .put("key", "device-smoke-test-key-not-production")
              .put("client_id", "emulator")
              .put(
                  "groups",
                  new JSONArray()
                      .put(
                          new JSONObject()
                              .put("name", "Primary")
                              .put("resolvers", new JSONArray().put("10.0.2.2:15353"))))
              .put("window", 8)
              .put("timeout_ms", 1000)
              .put("adaptive", true);
      ProfileStore.save(context, new JSONArray().put(profile));
      activity =
          (MainActivity)
              startActivitySync(
                  new Intent(context, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
      waitForIdleSync();
      screenshot("connection");
      if (VpnService.prepare(context) != null)
        throw new AssertionError("Grant ACTIVATE_VPN app-op before running this test");
      for (int cycle = 0; cycle < 3; cycle++) {
        tapButton("Connect");
        await("Connected", 30000);
        SystemClock.sleep(300);
        if (TunnelService.instance == null)
          throw new AssertionError("Service disappeared after connect");
        if (cycle == 0) {
          screenshot("connected");
          String target = options.getString("target");
          if (target == null)
            throw new AssertionError("Pass the local echo server IP with -e target");
          try (java.net.Socket socket = new java.net.Socket()) {
            socket.connect(new java.net.InetSocketAddress(target, 18080), 15000);
            socket.setSoTimeout(15000);
            byte[] payload = new byte[16384];
            for (int i = 0; i < payload.length; i++) payload[i] = (byte) i;
            socket.getOutputStream().write(payload);
            byte[] reply = new byte[payload.length];
            new java.io.DataInputStream(socket.getInputStream()).readFully(reply);
            if (!java.util.Arrays.equals(payload, reply))
              throw new AssertionError("VPN payload corrupted");
          }
          SystemClock.sleep(1200);
          JSONObject stats = new JSONObject(TunnelService.stats);
          if (stats.optLong("uploaded") < 16384 || stats.optLong("downloaded") < 16384)
            throw new AssertionError("Transfer bypassed DNS transport: " + stats);
        }
        tapButton("Disconnect");
        await("Disconnected", 5000);
        if (TunnelService.instance != null)
          throw new AssertionError("Service retained resources after disconnect");
        for (Network n : context.getSystemService(ConnectivityManager.class).getAllNetworks()) {
          NetworkCapabilities caps =
              context.getSystemService(ConnectivityManager.class).getNetworkCapabilities(n);
          if (caps != null && caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN))
            throw new AssertionError("VPN network survived disconnect");
        }
      }
      tapButton("Edit selected server");
      screenshot("server-editor");
      runOnMainSync(() -> activity.onBackPressed());
      tapText("Scan");
      screenshot("scanner");
      tapText("Routing");
      screenshot("routing");
      tapButton("Add rule");
      screenshot("rule-editor");
      runOnMainSync(() -> activity.onBackPressed());
      // Cancel an unreachable startup and ensure the worker cannot resurrect it.
      JSONObject offline = new JSONObject(profile.toString());
      offline.put("automatic_groups", true);
      offline.put(
          "groups",
          new JSONArray()
              .put(
                  new JSONObject()
                      .put("name", "Unavailable")
                      .put("resolvers", new JSONArray().put("10.0.2.2:15999"))));
      runOnMainSync(
          () -> {
            Intent intent =
                new Intent(context, TunnelService.class).putExtra("profile", offline.toString());
            if (Build.VERSION.SDK_INT >= 26) context.startForegroundService(intent);
            else context.startService(intent);
          });
      await("Connecting", 3000);
      runOnMainSync(() -> TunnelService.disconnect(context));
      await("Disconnected", 3000);
      SystemClock.sleep(1000);
      if (TunnelService.instance != null || !TunnelService.state.equals("Disconnected"))
        throw new AssertionError("Canceled startup resurrected VPN");
      result.putString(
          "stream",
          "PASS: three UI connect/disconnect cycles; VPN network removed; startup cancellation; all"
              + " screen captures\n");
      finish(Activity.RESULT_OK, result);
    } catch (Throwable error) {
      result.putString("stream", "FAIL: " + android.util.Log.getStackTraceString(error));
      finish(1, result);
    }
  }

  private void await(String expected, long timeout) {
    long deadline = SystemClock.uptimeMillis() + timeout;
    while (!TunnelService.state.equals(expected) && SystemClock.uptimeMillis() < deadline)
      SystemClock.sleep(50);
    if (!TunnelService.state.equals(expected))
      throw new AssertionError("Expected " + expected + ", got " + TunnelService.state);
    waitForIdleSync();
    SystemClock.sleep(1100);
  }

  private void tapButton(String title) {
    tap(title, true);
  }

  private void tapText(String title) {
    tap(title, false);
  }

  private void tap(String title, boolean button) {
    runOnMainSync(
        () -> {
          View view = find(activity.getWindow().getDecorView(), title, button);
          if (view == null) throw new AssertionError("Missing control: " + title);
          if (!view.isEnabled()) throw new AssertionError("Disabled control: " + title);
          view.performClick();
        });
    waitForIdleSync();
    SystemClock.sleep(200);
  }

  private View find(View view, String title, boolean button) {
    if (view instanceof TextView
        && (!button || view instanceof Button)
        && ((TextView) view).getText().toString().equals(title)) {
      if (!button && view.getParent() instanceof View && !view.isClickable())
        return (View) view.getParent();
      return view;
    }
    if (view instanceof ViewGroup)
      for (int i = 0; i < ((ViewGroup) view).getChildCount(); i++) {
        View found = find(((ViewGroup) view).getChildAt(i), title, button);
        if (found != null) return found;
      }
    return null;
  }

  private void screenshot(String name) throws Exception {
    waitForIdleSync();
    SystemClock.sleep(300);
    Bitmap bitmap = getUiAutomation().takeScreenshot();
    if (bitmap == null) throw new IOException("Screenshot unavailable");
    File dir = new File(getTargetContext().getExternalFilesDir(null), "smoke-screenshots");
    dir.mkdirs();
    try (FileOutputStream out = new FileOutputStream(new File(dir, name + ".png"))) {
      bitmap.compress(Bitmap.CompressFormat.PNG, 100, out);
    }
    bitmap.recycle();
  }
}
