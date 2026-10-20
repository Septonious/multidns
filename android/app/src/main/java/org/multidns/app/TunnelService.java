package org.multidns.app;

import android.app.*;
import android.content.Intent;
import android.net.*;
import android.os.*;
import android.util.Log;
import mobile.Engine;
import mobile.Mobile;
import org.json.*;

public final class TunnelService extends VpnService {
  static volatile String state = "Disconnected";
  static volatile String stats = "{}";
  static volatile TunnelService instance;
  private volatile boolean stopping;
  private Engine engine;
  private ParcelFileDescriptor tun;
  private Thread nativeThread;
  private ConnectivityManager connectivity;
  private volatile Network network;
  private ConnectivityManager.NetworkCallback callback;
  private final Handler handler = new Handler(Looper.getMainLooper());

  static Notification notification(android.content.Context c, String title, int id) {
    NotificationManager nm = c.getSystemService(NotificationManager.class);
    if (Build.VERSION.SDK_INT >= 26)
      nm.createNotificationChannel(
          new NotificationChannel("multidns", "MultiDNS", NotificationManager.IMPORTANCE_LOW));
    PendingIntent open =
        PendingIntent.getActivity(
            c,
            id,
            new Intent(c, MainActivity.class),
            PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
    Notification.Builder builder =
        Build.VERSION.SDK_INT >= 26
            ? new Notification.Builder(c, "multidns")
            : new Notification.Builder(c);
    builder
        .setContentTitle(title)
        .setSmallIcon(org.multidns.app.R.drawable.ic_status)
        .setContentIntent(open)
        .setOngoing(true)
        .setOnlyAlertOnce(true);
    if (id == 1) {
      PendingIntent disconnect =
          PendingIntent.getService(
              c,
              10,
              new Intent(c, TunnelService.class).setAction("STOP"),
              PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
      builder.addAction(new Notification.Action.Builder(null, "Disconnect", disconnect).build());
    }
    return builder.build();
  }

  @Override
  public int onStartCommand(Intent intent, int flags, int startId) {
    if (intent == null || "STOP".equals(intent.getAction())) {
      shutdown();
      return START_NOT_STICKY;
    }
    if (engine != null || stopping || (instance != null && instance != this)) {
      if (instance != this) stopSelf();
      return START_NOT_STICKY;
    }
    instance = this;
    state = "Connecting";
    stats = "{}";
    try {
      startForeground(1, notification(this, "MultiDNS · Connecting", 1));
      engine = Mobile.newEngine();
      connectivity = getSystemService(ConnectivityManager.class);
      for (Network n : connectivity.getAllNetworks()) {
        NetworkCapabilities caps = connectivity.getNetworkCapabilities(n);
        if (caps != null
            && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)) {
          network = n;
          break;
        }
      }
      callback =
          new ConnectivityManager.NetworkCallback() {
            @Override
            public void onAvailable(Network n) {
              handler.post(
                  () -> {
                    if (stopping) return;
                    if (n.equals(network)) return;
                    network = n;
                    setUnderlyingNetworks(new Network[] {n});
                    final Engine current = engine;
                    if (current != null)
                      new Thread(current::networkChanged, "multidns-rebind").start();
                    if (tun != null) state = "Connected";
                  });
            }

            @Override
            public void onLost(Network n) {
              handler.post(
                  () -> {
                    if (!stopping && n.equals(network)) {
                      network = null;
                      if (tun != null) state = "Waiting for network";
                    }
                  });
            }
          };
      connectivity.registerNetworkCallback(
          new NetworkRequest.Builder()
              .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
              .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
              .build(),
          callback);
      final String config = intent.getStringExtra("profile");
      final Engine current = engine;
      new Thread(
              () -> {
                try {
                  Mobile.validate(config);
                  NativeTunnel.ensureLoaded();
                  if (stopping) return;
                  current.start(config, 10808, fd -> protectFD((int) fd));
                  handler.post(() -> establish(config));
                } catch (Exception | LinkageError e) {
                  handler.post(() -> connectionFailed("Connection", e));
                }
              },
              "multidns-connect")
          .start();
    } catch (Exception | LinkageError e) {
      connectionFailed("VPN startup", e);
    }
    return START_NOT_STICKY;
  }

  // All resource publication and VPN lifecycle changes happen on the main thread.
  // A canceled startup can never establish or resurrect the VPN afterward.
  private void establish(String config) {
    if (stopping) return;
    try {
      JSONObject p = new JSONObject(config);
      Builder b =
          new Builder()
              .setSession("MultiDNS · " + p.optString("name", "Server"))
              .setMtu(1280)
              .addAddress("10.77.0.2", 30)
              .addAddress("fd77::2", 126)
              .addRoute("0.0.0.0", 0)
              .addRoute("::", 0)
              .addDnsServer("1.1.1.1");
      JSONArray apps = p.optJSONArray("apps");
      String mode = p.optString("app_mode", "all");
      for (int i = 0; apps != null && i < apps.length(); i++) {
        if ("include".equals(mode)) b.addAllowedApplication(apps.getString(i));
        else if ("exclude".equals(mode)) b.addDisallowedApplication(apps.getString(i));
      }
      if (network != null) b.setUnderlyingNetworks(new Network[] {network});
      tun = b.establish();
      if (tun == null) throw new Exception("VPN permission was revoked");
      final int fd = tun.getFd();
      final String yaml =
          "tunnel:\n"
              + "  mtu: 1280\n"
              + "  ipv4: 10.77.0.2\n"
              + "  ipv6: 'fd77::2'\n"
              + "socks5:\n"
              + "  address: 127.0.0.1\n"
              + "  port: 10808\n"
              + "  udp: 'udp'\n"
              + "misc:\n"
              + "  task-stack-size: 65536\n"
              + "  connect-timeout: 15000\n"
              + "  tcp-read-write-timeout: 180000\n"
              + "  udp-read-write-timeout: 60000\n"
              + "  log-level: warn\n";
      nativeThread =
          new Thread(
              () -> {
                try {
                  int result = NativeTunnel.run(yaml, fd);
                  handler.post(
                      () -> {
                        if (!stopping)
                          connectionFailed(
                              "VPN adapter", new Exception("Stopped (" + result + ")"));
                      });
                } catch (Exception | LinkageError e) {
                  handler.post(() -> connectionFailed("VPN adapter", e));
                }
              },
              "multidns-tun");
      nativeThread.start();
      state = "Connected";
      getSystemService(NotificationManager.class)
          .notify(1, notification(this, "MultiDNS · Connected", 1));
      handler.post(tick);
    } catch (Exception | LinkageError e) {
      connectionFailed("VPN interface", e);
    }
  }

  private void connectionFailed(String stage, Throwable error) {
    if (stopping) return;
    Log.e("MultiDNS", stage + " failed", error);
    state = "Connection failed: " + stage + ": " + error.getMessage();
    shutdown();
  }

  private final Runnable tick =
      new Runnable() {
        public void run() {
          final Engine current = engine;
          if (current == null || stopping) return;
          new Thread(
                  () -> {
                    String snapshot = current.stats();
                    handler.post(
                        () -> {
                          if (!stopping) {
                            stats = snapshot;
                            handler.postDelayed(tick, 1000);
                          }
                        });
                  },
                  "multidns-stats")
              .start();
        }
      };

  boolean protectFD(int fd) {
    try {
      if (stopping || !protect(fd)) return false;
      Network n = network;
      if (n != null) {
        try (ParcelFileDescriptor p = ParcelFileDescriptor.fromFd(fd)) {
          n.bindSocket(p.getFileDescriptor());
        }
      }
      return !stopping;
    } catch (Exception e) {
      return false;
    }
  }

  @Override
  public void onRevoke() {
    state = "VPN permission revoked";
    shutdown();
  }

  static void disconnect(android.content.Context context) {
    TunnelService current = instance;
    if (current != null) current.handler.post(current::shutdown);
    else context.stopService(new Intent(context, TunnelService.class));
  }

  private void shutdown() {
    if (stopping) return;
    stopping = true;
    final boolean normal =
        !state.startsWith("Connection failed") && !state.equals("VPN permission revoked");
    if (normal) state = "Disconnecting";
    handler.removeCallbacks(tick);
    if (connectivity != null && callback != null) {
      try {
        connectivity.unregisterNetworkCallback(callback);
      } catch (IllegalArgumentException ignored) {
      }
      callback = null;
    }
    // Release the VPN now; never put transport cleanup or a thread join in front of it.
    if (nativeThread != null) {
      try {
        NativeTunnel.stop();
      } catch (LinkageError e) {
        Log.e("MultiDNS", "Adapter cleanup", e);
      }
    }
    if (tun != null) {
      try {
        tun.close();
      } catch (Exception ignored) {
      }
      tun = null;
    }
    stopForeground(STOP_FOREGROUND_REMOVE);
    final Engine current = engine;
    engine = null;
    new Thread(
            () -> {
              if (current != null) current.stop();
              if (nativeThread != null) {
                try {
                  nativeThread.join();
                } catch (InterruptedException e) {
                  Thread.currentThread().interrupt();
                }
              }
              handler.post(
                  () -> {
                    if (instance == this) {
                      instance = null;
                      stats = "{}";
                      if (normal) state = "Disconnected";
                    }
                    stopSelf();
                  });
            },
            "multidns-shutdown")
        .start();
  }

  @Override
  public void onDestroy() {
    shutdown();
    super.onDestroy();
  }
}
