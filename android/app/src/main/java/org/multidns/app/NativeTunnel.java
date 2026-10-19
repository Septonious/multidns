package org.multidns.app;
public final class NativeTunnel {
 static { System.loadLibrary("hev-socks5-tunnel"); System.loadLibrary("multidns-tun"); }
 private NativeTunnel() {}
 public static native int run(String config,int fd);
 public static native void stop();
}
