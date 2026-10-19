MULTIDNS_PATH := $(call my-dir)
include $(HEV_PATH)/Android.mk
LOCAL_PATH := $(MULTIDNS_PATH)
include $(CLEAR_VARS)
LOCAL_MODULE := multidns-tun
LOCAL_SRC_FILES := tunnel.c
LOCAL_C_INCLUDES := $(HEV_PATH)/include
LOCAL_SHARED_LIBRARIES := hev-socks5-tunnel
LOCAL_LDFLAGS += -Wl,-z,max-page-size=16384
include $(BUILD_SHARED_LIBRARY)
