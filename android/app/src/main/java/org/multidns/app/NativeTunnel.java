package org.multidns.app;

public final class NativeTunnel {
  static {
    System.loadLibrary("hev-socks5-tunnel");
    System.loadLibrary("multidns-tun");
  }

  private NativeTunnel() {}

  // Trigger class initialization inside the connection worker's error handler,
  // before a VPN interface is established or the native thread is launched.
  public static void ensureLoaded() {}

  public static native int run(String config, int fd);

  public static native void stop();
}
