package org.multidns.app;

import java.io.*;
import java.util.concurrent.*;
import java.util.regex.*;
import mobile.Mobile;
import org.json.*;

/** Explicit, cancellable probes while disconnected. ICMP is optional on Android. */
final class LatencyTester {
  interface Events {
    void row(String endpoint, JSONObject result);

    void finished();

    void failed(String message);
  }

  private final ExecutorService pings = Executors.newFixedThreadPool(4);
  private final java.util.Set<Process> processes = ConcurrentHashMap.newKeySet();
  private volatile boolean stopped;
  private final mobile.Engine engine = Mobile.newEngine();

  void stop() {
    stopped = true;
    engine.stop();
    pings.shutdownNow();
    for (Process process : processes) process.destroy();
  }

  void run(String config, Events events) {
    new Thread(
            () -> {
              try {
                engine.testLatency(
                    config,
                    message -> {
                      if (stopped) return;
                      try {
                        JSONObject result = new JSONObject(message);
                        String endpoint = result.getString("endpoint");
                        result.put("icmp_status", "Testing");
                        events.row(endpoint, new JSONObject(result.toString()));
                        pings.submit(
                            () -> {
                              if (stopped) return;
                              double ms = ping(endpoint);
                              try {
                                result.put("icmp_ms", ms);
                                result.put(
                                    "icmp_status", ms >= 0 ? "Reply" : "No reply / unavailable");
                                if (!stopped) events.row(endpoint, result);
                              } catch (JSONException ignored) {
                              }
                            });
                      } catch (Exception ignored) {
                      }
                    });
              } catch (Exception error) {
                if (!stopped) events.failed(error.getMessage());
                // Failed E2E probes still emit rows; cancellation never reports success.
              } finally {
                pings.shutdown();
                try {
                  pings.awaitTermination(120, TimeUnit.SECONDS);
                } catch (InterruptedException e) {
                  Thread.currentThread().interrupt();
                }
                if (!stopped) events.finished();
              }
            },
            "multidns-latency")
        .start();
  }

  private double ping(String endpoint) {
    Process process = null;
    try {
      String host =
          endpoint.substring(0, endpoint.lastIndexOf(':')).replace("[", "").replace("]", "");
      process =
          new ProcessBuilder("/system/bin/ping", "-n", "-c", "2", "-W", "1", host)
              .redirectErrorStream(true)
              .start();
      processes.add(process);
      long deadline = android.os.SystemClock.elapsedRealtime() + 4000;
      while (true) {
        if (stopped || Thread.currentThread().isInterrupted()) return -1;
        try {
          process.exitValue();
          break;
        } catch (IllegalThreadStateException pending) {
        }
        if (android.os.SystemClock.elapsedRealtime() >= deadline) return -1;
        Thread.sleep(50);
      }
      ByteArrayOutputStream bytes = new ByteArrayOutputStream();
      byte[] buffer = new byte[1024];
      int n;
      while ((n = process.getInputStream().read(buffer)) != -1) bytes.write(buffer, 0, n);
      String output = bytes.toString(java.nio.charset.StandardCharsets.UTF_8.name());
      Matcher matcher = Pattern.compile("time[=<]([0-9.]+)").matcher(output);
      double total = 0;
      int count = 0;
      while (matcher.find()) {
        total += Double.parseDouble(matcher.group(1));
        count++;
      }
      return count > 0 ? total / count : -1;
    } catch (Exception ignored) {
      return -1;
    } finally {
      if (process != null) {
        processes.remove(process);
        process.destroy();
      }
    }
  }
}
