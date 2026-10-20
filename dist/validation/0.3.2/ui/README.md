Headless Android layout evidence, 0.3.2

Robolectric 4.14.1 / SDK35 / native graphics. Fixture profiles and connected-state
counters; mobile.Mobile is shadowed to avoid loading an Android Go library on the
host. This is not physical-device/VPN verification. The test creates no real VPN.

360dp screens: connection, connected fixture, scanner, routing, diagnostics,
server editor. 320dp / 1.3 font-scale: connection and diagnostics. Each render
asserts navigation touch height >=48dp and no label ellipsis. PNGs show the view
root without device system bars. Rendered profile key is a nonproduction fixture.

The init script injects test-only dependencies/source; product Gradle dependencies
are unchanged. To repeat elsewhere, update the absolute source/output paths in
renderer.init.gradle and LayoutRenderTest.java, then run from android:

./gradlew --no-daemon -Pandroid.useAndroidX=true -I /absolute/renderer.init.gradle :app:testDebugUnitTest --tests org.multidns.app.LayoutRenderTest

Maven access, trusted CA store and inherited approved proxy configuration may be
needed for one-off test dependencies. Do not disable TLS certificate validation.
