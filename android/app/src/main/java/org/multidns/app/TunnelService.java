package org.multidns.app;

import android.app.*;
import android.content.Intent;
import android.net.*;
import android.os.*;
import android.util.Log;
import java.util.concurrent.atomic.AtomicBoolean;
import mobile.Engine;
import mobile.Mobile;
import org.json.*;

public final class TunnelService extends VpnService {
  static volatile String state = "Disconnected";
  static volatile String stats = "{}";
  static volatile TunnelService instance;
  private final AtomicBoolean starting = new AtomicBoolean();
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
    return builder
        .setContentTitle(title)
        .setSmallIcon(android.R.drawable.stat_sys_upload_done)
        .setContentIntent(open)
        .setOngoing(true)
        .build();
  }

  @Override
  public int onStartCommand(Intent intent, int flags, int startId) {
    if (intent == null) return START_NOT_STICKY;
    if ("STOP".equals(intent.getAction())) {
      stopSelf();
      return START_NOT_STICKY;
    }
    try {
      startForeground(1, notification(this, "MultiDNS connecting", 1));
    } catch (RuntimeException e) {
      connectionFailed("VPN service startup", e);
      return START_NOT_STICKY;
    }
    if (!starting.compareAndSet(false, true)) return START_NOT_STICKY;
    stopping = false;
    instance = this;
    String config = intent.getStringExtra("profile");
    new Thread(() -> connect(config), "multidns-connect").start();
    return START_NOT_STICKY;
  }

  private void connect(String config) {
    try {
      state = "Connecting";
      JSONObject p = new JSONObject(config);
      Mobile.validate(config);
      NativeTunnel.ensureLoaded();
      connectivity = getSystemService(ConnectivityManager.class);
      NetworkRequest request =
          new NetworkRequest.Builder()
              .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
              .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
              .build();
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
              network = n;
              setUnderlyingNetworks(new Network[] {n});
              if (engine != null)
                new Thread(() -> engine.networkChanged(), "multidns-rebind").start();
              if (tun != null) state = "Connected";
            }

            @Override
            public void onLost(Network n) {
              if (n.equals(network)) {
                network = null;
                state = "Waiting for network";
              }
            }
          };
      connectivity.registerNetworkCallback(request, callback);
      engine = Mobile.newEngine();
      engine.start(config, 10808, fd -> protectFD((int) fd));
      if (stopping) {
        engine.stop();
        return;
      }
      Builder b =
          new Builder()
              .setSession("MultiDNS · " + p.optString("name", "server"))
              .setMtu(1280)
              .addAddress("10.77.0.2", 30)
              .addAddress("fd77::2", 126)
              .addRoute("0.0.0.0", 0)
              .addRoute("::", 0)
              .addDnsServer("1.1.1.1");
      JSONArray apps = p.optJSONArray("apps");
      String mode = p.optString("app_mode", "all");
      if ("include".equals(mode)) {
        for (int i = 0; apps != null && i < apps.length(); i++)
          b.addAllowedApplication(apps.getString(i));
      } else if ("exclude".equals(mode)) {
        for (int i = 0; apps != null && i < apps.length(); i++)
          b.addDisallowedApplication(apps.getString(i));
      }
      if (network != null) b.setUnderlyingNetworks(new Network[] {network});
      tun = b.establish();
      if (tun == null) throw new Exception("VPN permission was revoked");
      if (stopping) {
        tun.close();
        return;
      }
      String yaml =
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
              + "  read-write-timeout: 180000\n"
              + "  log-level: warn\n";
      final int fd = tun.getFd();
      nativeThread =
          new Thread(
              () -> {
                try {
                  int result = NativeTunnel.run(yaml, fd);
                  if (!stopping) {
                    state = "VPN adapter stopped (" + result + ")";
                    stopSelf();
                  }
                } catch (Exception | LinkageError e) {
                  connectionFailed("VPN adapter startup", e);
                }
              },
              "multidns-tun");
      state = "Connected";
      nativeThread.start();
      getSystemService(NotificationManager.class)
          .notify(1, notification(this, "MultiDNS connected", 1));
      handler.post(tick);
    } catch (Exception | LinkageError e) {
      connectionFailed("VPN connection", e);
    }
  }

  private void connectionFailed(String stage, Throwable error) {
    Log.e("MultiDNS", stage + " failed", error);
    if (!stopping) state = "Connection failed: " + stage + ": " + error;
    stopSelf();
  }

  private final Runnable tick =
      new Runnable() {
        public void run() {
          if (engine != null && !stopping) {
            stats = engine.stats();
            handler.postDelayed(this, 1000);
          }
        }
      };

  boolean protectFD(int fd) {
    try {
      if (!protect(fd)) return false;
      Network n = network;
      if (n != null) {
        ParcelFileDescriptor p = ParcelFileDescriptor.fromFd(fd);
        try {
          n.bindSocket(p.getFileDescriptor());
        } finally {
          p.close();
        }
      }
      return true;
    } catch (Exception e) {
      return false;
    }
  }

  @Override
  public void onRevoke() {
    state = "VPN permission revoked";
    stopSelf();
  }

  @Override
  public void onDestroy() {
    stopping = true;
    handler.removeCallbacks(tick);
    if (connectivity != null && callback != null)
      try {
        connectivity.unregisterNetworkCallback(callback);
      } catch (IllegalArgumentException e) {
        Log.w("MultiDNS", "Network callback was already unregistered", e);
      }
    new Thread(
            () -> {
              if (engine != null) engine.stop();
              if (nativeThread != null) {
                try {
                  NativeTunnel.stop();
                } catch (LinkageError e) {
                  Log.e("MultiDNS", "VPN adapter cleanup failed", e);
                }
                try {
                  nativeThread.join(2000);
                } catch (InterruptedException ignored) {
                  Thread.currentThread().interrupt();
                }
              }
              if (tun != null)
                try {
                  tun.close();
                } catch (Exception ignored) {
                }
              if (instance == this) instance = null;
              starting.set(false);
              if ("Connected".equals(state)
                  || "Connecting".equals(state)
                  || "Disconnecting".equals(state)) state = "Disconnected";
              stats = "{}";
            },
            "multidns-shutdown")
        .start();
    if ("Connected".equals(state) || "Connecting".equals(state)) state = "Disconnecting";
    super.onDestroy();
  }
}
