MULTIDNS_PATH := $(call my-dir)
# Build the HEV C API only. Upstream's Android.mk also includes hev-jni.c,
# whose JNI_OnLoad registers methods on hev/htproxy/TProxyService. MultiDNS
# uses its own JNI bridge and does not provide that upstream Java class.
include $(HEV_PATH)/third-part/yaml/Android.mk
include $(HEV_PATH)/third-part/lwip/Android.mk
ENABLE_MEMALLOC_SLICE := 0
include $(HEV_PATH)/third-part/hev-task-system/Android.mk

LOCAL_PATH := $(HEV_PATH)
SRCDIR := $(LOCAL_PATH)/src
include $(HEV_PATH)/build.mk
include $(CLEAR_VARS)
LOCAL_MODULE := hev-socks5-tunnel
LOCAL_SRC_FILES := $(patsubst $(SRCDIR)/%,src/%,$(filter-out $(SRCDIR)/hev-jni.c,$(SRCFILES)))
LOCAL_C_INCLUDES := \
    $(HEV_PATH)/src \
    $(HEV_PATH)/src/misc \
    $(HEV_PATH)/src/core/include \
    $(HEV_PATH)/third-part/yaml/include \
    $(HEV_PATH)/third-part/lwip/src/include \
    $(HEV_PATH)/third-part/lwip/src/ports/include \
    $(HEV_PATH)/third-part/hev-task-system/include
LOCAL_CFLAGS := -DFD_SET_DEFINED -DSOCKLEN_T_DEFINED -DENABLE_LIBRARY
LOCAL_CFLAGS += -fno-stack-clash-protection $(VERSION_CFLAGS)
LOCAL_STATIC_LIBRARIES := yaml lwip hev-task-system
LOCAL_LDFLAGS := -Wl,-z,max-page-size=16384 -Wl,-z,common-page-size=16384
include $(BUILD_SHARED_LIBRARY)

LOCAL_PATH := $(MULTIDNS_PATH)
include $(CLEAR_VARS)
LOCAL_MODULE := multidns-tun
LOCAL_SRC_FILES := tunnel.c
LOCAL_C_INCLUDES := $(HEV_PATH)/include
LOCAL_SHARED_LIBRARIES := hev-socks5-tunnel
LOCAL_LDFLAGS += -Wl,-z,max-page-size=16384
include $(BUILD_SHARED_LIBRARY)
