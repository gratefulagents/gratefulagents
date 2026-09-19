#include "computer_use_window.h"
#include <dlfcn.h>
#include <math.h>
#include <pthread.h>

static AXError (*windowID)(AXUIElementRef, CGWindowID *);
static pthread_once_t windowIDOnce = PTHREAD_ONCE_INIT;

static void resolveWindowID(void) {
    // There is no public AX-to-CG ID API. Missing support must never fall back
    // to geometry or window ordering: overlays can share both with the target.
    windowID = (AXError (*)(AXUIElementRef, CGWindowID *))dlsym(RTLD_DEFAULT, "_AXUIElementGetWindow");
}

static CFTypeRef attribute(AXUIElementRef element, CFStringRef name) {
    CFTypeRef value = NULL;
    if (AXUIElementCopyAttributeValue(element, name, &value) != kAXErrorSuccess) {
        if (value) CFRelease(value);
        return NULL;
    }
    return value;
}

bool ga_ax_window_matches(AXUIElementRef window, pid_t pid, CGWindowID window_id, CGRect bounds) {
    if (!window || pid <= 0 || window_id == kCGNullWindowID ||
        CFGetTypeID(window) != AXUIElementGetTypeID() ||
        !isfinite(bounds.origin.x) || !isfinite(bounds.origin.y) ||
        !isfinite(bounds.size.width) || !isfinite(bounds.size.height) ||
        bounds.size.width <= 0 || bounds.size.height <= 0 ||
        AXUIElementSetMessagingTimeout(window, 0.2f) != kAXErrorSuccess) return false;
    pid_t owner = 0;
    if (AXUIElementGetPid(window, &owner) != kAXErrorSuccess || owner != pid) return false;
    pthread_once(&windowIDOnce, resolveWindowID);
    CGWindowID actual = kCGNullWindowID;
    if (!windowID || windowID(window, &actual) != kAXErrorSuccess || actual != window_id) return false;

    CFTypeRef role = attribute(window, kAXRoleAttribute);
    CFTypeRef position = attribute(window, kAXPositionAttribute);
    CFTypeRef size = attribute(window, kAXSizeAttribute);
    CGPoint point = {0};
    CGSize dimensions = {0};
    bool matches = role && CFEqual(role, kAXWindowRole) && position && size &&
        CFGetTypeID(position) == AXValueGetTypeID() && CFGetTypeID(size) == AXValueGetTypeID() &&
        AXValueGetType(position) == kAXValueCGPointType && AXValueGetType(size) == kAXValueCGSizeType &&
        AXValueGetValue(position, kAXValueCGPointType, &point) &&
        AXValueGetValue(size, kAXValueCGSizeType, &dimensions) &&
        point.x == bounds.origin.x && point.y == bounds.origin.y &&
        dimensions.width == bounds.size.width && dimensions.height == bounds.size.height;
    if (role) CFRelease(role);
    if (position) CFRelease(position);
    if (size) CFRelease(size);
    return matches;
}

AXUIElementRef ga_ax_window_copy(pid_t pid, CGWindowID window_id, CGRect bounds, AXUIElementRef retained, GAWindowEligibility *eligibility) {
    if (eligibility) *eligibility = GAWindowIdentityUnavailable;
    AXUIElementRef app = AXUIElementCreateApplication(pid);
    if (!app) return NULL;
    if (AXUIElementSetMessagingTimeout(app, 0.2f) != kAXErrorSuccess) {
        CFRelease(app);
        return NULL;
    }
    CFTypeRef focused = attribute(app, kAXFocusedWindowAttribute);
    AXUIElementRef result = NULL;
    if (ga_ax_window_matches((AXUIElementRef)focused, pid, window_id, bounds)) {
        result = (AXUIElementRef)CFRetain(focused);
        if (eligibility) *eligibility = GAWindowFocused;
    } else {
        CFTypeRef windows = attribute(app, kAXWindowsAttribute);
        if (windows && CFGetTypeID(windows) == CFArrayGetTypeID()) {
            for (CFIndex i = 0; i < CFArrayGetCount(windows); i++) {
                AXUIElementRef window = (AXUIElementRef)CFArrayGetValueAtIndex(windows, i);
                if (ga_ax_window_matches(window, pid, window_id, bounds)) {
                    result = (AXUIElementRef)CFRetain(window);
                    if (eligibility) *eligibility = GAWindowUnfocused;
                    break;
                }
            }
        }
        if (windows) CFRelease(windows);
    }
    if (focused) CFRelease(focused);
    CFRelease(app);
    if (result && retained && (!CFEqual(retained, result) ||
        !ga_ax_window_matches(retained, pid, window_id, bounds))) {
        CFRelease(result);
        result = NULL;
        if (eligibility) *eligibility = GAWindowIdentityUnavailable;
    }
    return result;
}
