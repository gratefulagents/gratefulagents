#ifndef GA_TEST_APPLICATION_SERVICES_H
#define GA_TEST_APPLICATION_SERVICES_H

/* Fault-injection API fixture only, not an Apple ABI substitute. */
#include <stdbool.h>
#include <stdint.h>
#include <stddef.h>
#include <sys/types.h>

typedef const void *CFTypeRef;
typedef const void *CFStringRef;
typedef const void *AXUIElementRef;
typedef const void *AXValueRef;
typedef const void *CFArrayRef;
typedef unsigned long CFTypeID;
typedef long CFIndex;
typedef int AXError;
typedef int AXValueType;
typedef uint32_t CGWindowID;
typedef struct { double x, y; } CGPoint;
typedef struct { double width, height; } CGSize;
typedef struct { CGPoint origin; CGSize size; } CGRect;

enum { kAXErrorSuccess = 0, kAXValueCGPointType = 1, kAXValueCGSizeType = 2, kCGNullWindowID = 0 };
extern CFStringRef kAXRoleAttribute, kAXPositionAttribute, kAXSizeAttribute;
extern CFStringRef kAXFocusedWindowAttribute, kAXWindowsAttribute, kAXWindowRole;

CFTypeID CFGetTypeID(CFTypeRef value);
bool CFEqual(CFTypeRef a, CFTypeRef b);
void CFRelease(CFTypeRef value);
CFTypeID CFArrayGetTypeID(void);
CFIndex CFArrayGetCount(CFArrayRef array);
CFTypeRef CFArrayGetValueAtIndex(CFArrayRef array, CFIndex index);
CFTypeID AXUIElementGetTypeID(void);
AXUIElementRef AXUIElementCreateApplication(pid_t pid);
AXError AXUIElementCopyAttributeValue(AXUIElementRef element, CFStringRef name, CFTypeRef *value);
AXError AXUIElementSetMessagingTimeout(AXUIElementRef element, float timeout);
AXError AXUIElementGetPid(AXUIElementRef element, pid_t *pid);
CFTypeID AXValueGetTypeID(void);
AXValueType AXValueGetType(AXValueRef value);
bool AXValueGetValue(AXValueRef value, AXValueType type, void *result);
#endif
