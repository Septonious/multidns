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
@Config(sdk=35,shadows={LayoutRenderTest.MobileShadow.class},instrumentedPackages={"mobile"})
@GraphicsMode(GraphicsMode.Mode.NATIVE)
public class LayoutRenderTest {
 @Implements(value=Mobile.class,isInAndroidSdk=false)
 public static class MobileShadow {
  @Implementation protected static void __staticInitializer__() {}
  @Implementation protected static String normalizeResolver(String endpoint) {return endpoint.contains(":")?endpoint:endpoint+":53";}
 }
 static void set(Object object,String name,Object value) throws Exception {Field f=object.getClass().getDeclaredField(name);f.setAccessible(true);f.set(object,value);}
 static Object get(Object object,String name) throws Exception {Field f=object.getClass().getDeclaredField(name);f.setAccessible(true);return f.get(object);}
 static void page(MainActivity activity,String name) throws Exception {Method m=MainActivity.class.getDeclaredMethod(name);m.setAccessible(true);m.invoke(activity);}
 static JSONObject profile() throws Exception {
  JSONArray groups=new JSONArray();for(int i=0;i<4;i++)groups.put(new JSONObject().put("name","Group "+(i+1)).put("resolvers",new JSONArray().put("192.0.2."+(i*2+1)+":53").put("192.0.2."+(i*2+2)+":53")));
  return new JSONObject().put("name","Atmoslink").put("domain","t.example.test").put("key","ui-fixture-key-not-production").put("groups",groups).put("rules",new JSONArray()).put("default_action","tunnel").put("adaptive",true).put("window",8);
 }
 static void render(MainActivity activity,String name,int width,int height) throws Exception {
  View root=(View)get(activity,"root");root.measure(View.MeasureSpec.makeMeasureSpec(width,View.MeasureSpec.EXACTLY),View.MeasureSpec.makeMeasureSpec(height,View.MeasureSpec.EXACTLY));root.layout(0,0,width,height);
  Bitmap bitmap=Bitmap.createBitmap(width,height,Bitmap.Config.ARGB_8888);root.draw(new Canvas(bitmap));File folder=new File("/workspace/multidns/work/ui");folder.mkdirs();try(FileOutputStream out=new FileOutputStream(new File(folder,name+".png"))){bitmap.compress(Bitmap.CompressFormat.PNG,100,out);}bitmap.recycle();
  assertEquals(width,root.getWidth());
  for(Object item:(java.util.List<?>)get(activity,"navItems")){View view=(View)item;assertTrue("navigation touch target",view.getHeight()>=48);}
  for(Object label:(java.util.List<?>)get(activity,"navLabels")){TextView view=(TextView)label;assertNotNull(view.getLayout());assertEquals("navigation label clipped: "+view.getText(),0,view.getLayout().getEllipsisCount(0));}
 }
 @Test public void screensAt360dp() throws Exception {
  RuntimeEnvironment.setQualifiers("w360dp-h800dp-mdpi");
  var controller=Robolectric.buildActivity(MainActivity.class).setup();MainActivity activity=controller.get();set(activity,"profiles",new JSONArray().put(profile()));set(activity,"selected",0);
  for(String name:new String[]{"servers","scanner","routing","diagnostics"}){page(activity,name);render(activity,name,360,800);}
  Method edit=MainActivity.class.getDeclaredMethod("edit",int.class);edit.setAccessible(true);edit.invoke(activity,0);render(activity,"editor",360,800);
  TunnelService.state="Connected";TunnelService.instance=new TunnelService();TunnelService.stats="{\"transport_version\":\"0.3.2\",\"queries\":1000,\"exchanges\":1000,\"uploaded\":2200000,\"downloaded\":6960000,\"streams\":8,\"retries\":3,\"timeouts\":2,\"groups\":[{},{},{},{}],\"resolvers\":[{\"healthy\":true},{\"healthy\":true},{\"healthy\":true},{\"healthy\":true},{\"healthy\":true},{\"healthy\":true},{\"healthy\":true},{\"healthy\":true}]}";
  ShadowSystemClock.advanceBy(Duration.ofSeconds(2));page(activity,"servers");set(activity,"statsAt",android.os.SystemClock.elapsedRealtime()-1000);set(activity,"statsUploaded",2120000L);set(activity,"statsDownloaded",6520000L);pageSummary(activity);render(activity,"connected",360,800);
  TunnelService.instance=null;TunnelService.state="Disconnected";TunnelService.stats="{}";controller.pause().stop().destroy();
 }
 static void pageSummary(MainActivity activity) throws Exception {Method m=MainActivity.class.getDeclaredMethod("connectionSummary");m.setAccessible(true);m.invoke(activity);}
 @Test public void narrowScreenLargeFont() throws Exception {
  RuntimeEnvironment.setQualifiers("w320dp-h720dp-mdpi");RuntimeEnvironment.setFontScale(1.3f);
  var controller=Robolectric.buildActivity(MainActivity.class).setup();MainActivity activity=controller.get();set(activity,"profiles",new JSONArray().put(profile()));set(activity,"selected",0);page(activity,"servers");render(activity,"narrow-font-130",320,720);page(activity,"diagnostics");render(activity,"diagnostics-narrow-font-130",320,720);controller.pause().stop().destroy();RuntimeEnvironment.setFontScale(1f);
 }
}
