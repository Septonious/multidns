#include <jni.h>
#include <string.h>
#include "hev-socks5-tunnel.h"

JNIEXPORT jint JNICALL Java_org_multidns_app_NativeTunnel_run(JNIEnv *env,jclass cls,jstring config,jint fd) {
 const char *s=(*env)->GetStringUTFChars(env,config,0);
 if (!s) return -1;
 int result=hev_socks5_tunnel_main_from_str((const unsigned char *)s,(unsigned int)strlen(s),fd);
 (*env)->ReleaseStringUTFChars(env,config,s);
 return result;
}
JNIEXPORT void JNICALL Java_org_multidns_app_NativeTunnel_stop(JNIEnv *env,jclass cls) { hev_socks5_tunnel_quit(); }
