#include "../src/computer_use_window.h"
#include <assert.h>
#include <math.h>
#include <stdio.h>
#include <string.h>

CFStringRef kAXRoleAttribute = "role", kAXPositionAttribute = "position", kAXSizeAttribute = "size";
CFStringRef kAXFocusedWindowAttribute = "focused", kAXWindowsAttribute = "windows", kAXWindowRole = "window";

enum { ElementType = 1, ValueType, ArrayType, OtherType };
typedef struct {
    CFTypeID type;
    AXValueType kind;
    double x, y;
} Value;
typedef struct {
    CFTypeID type;
    pid_t pid;
    CGWindowID id;
    CFStringRef role;
    Value position, size;
    bool id_error, pid_error, timeout_error, missing_position;
} Window;
typedef struct {
    CFTypeID type;
    CFTypeRef values[4];
    CFIndex count;
} Array;

static Window app, target, overlay;
static Array windows;
static CFTypeRef focused;
static bool missing_symbol, missing_app, missing_focus, missing_windows;
static unsigned tests;
static CGRect bounds = {{10, 20}, {300, 200}};

CFTypeID CFGetTypeID(CFTypeRef value) { return *(const CFTypeID *)value; }
bool CFEqual(CFTypeRef a, CFTypeRef b) { return a == b; }
void CFRelease(CFTypeRef value) { assert(value); }
CFTypeID CFArrayGetTypeID(void) { return ArrayType; }
CFIndex CFArrayGetCount(CFArrayRef array) { return ((const Array *)array)->count; }
CFTypeRef CFArrayGetValueAtIndex(CFArrayRef array, CFIndex index) { return ((const Array *)array)->values[index]; }
CFTypeID AXUIElementGetTypeID(void) { return ElementType; }
AXUIElementRef AXUIElementCreateApplication(pid_t pid) { return !missing_app && pid == app.pid ? &app : NULL; }
AXError AXUIElementSetMessagingTimeout(AXUIElementRef element, float timeout) {
    assert(timeout == 0.2f);
    return ((const Window *)element)->timeout_error ? -1 : 0;
}
AXError AXUIElementGetPid(AXUIElementRef element, pid_t *pid) {
    const Window *window = element;
    *pid = window->pid;
    return window->pid_error ? -1 : 0;
}
AXError AXUIElementCopyAttributeValue(AXUIElementRef element, CFStringRef name, CFTypeRef *value) {
    const Window *window = element;
    *value = NULL;
    if (element == &app) {
        if (name == kAXFocusedWindowAttribute && !missing_focus) *value = focused;
        if (name == kAXWindowsAttribute && !missing_windows) *value = &windows;
    } else {
        if (name == kAXRoleAttribute) *value = window->role;
        if (name == kAXPositionAttribute && !window->missing_position) *value = &window->position;
        if (name == kAXSizeAttribute) *value = &window->size;
    }
    return *value ? 0 : -1;
}
CFTypeID AXValueGetTypeID(void) { return ValueType; }
AXValueType AXValueGetType(AXValueRef value) { return ((const Value *)value)->kind; }
bool AXValueGetValue(AXValueRef value, AXValueType type, void *result) {
    const Value *v = value;
    if (type != v->kind) return false;
    if (type == kAXValueCGPointType) *(CGPoint *)result = (CGPoint){v->x, v->y};
    else *(CGSize *)result = (CGSize){v->x, v->y};
    return true;
}
static AXError getWindowID(AXUIElementRef element, CGWindowID *id) {
    const Window *window = element;
    *id = window->id;
    return window->id_error ? -1 : 0;
}
void *ga_test_dlsym(void *handle, const char *name) {
    (void)handle;
    assert(strcmp(name, "_AXUIElementGetWindow") == 0);
    return missing_symbol ? NULL : (void *)getWindowID;
}
static void reset(void) {
    target = (Window){.type = ElementType, .pid = 42, .id = 7, .role = kAXWindowRole,
        .position = {ValueType, kAXValueCGPointType, 10, 20},
        .size = {ValueType, kAXValueCGSizeType, 300, 200}};
    overlay = target;
    overlay.id = 8;
    app = target;
    focused = &target;
    windows = (Array){.type = ArrayType, .values = {&overlay, &target}, .count = 2};
    missing_app = missing_focus = missing_windows = false;
}
static void expect(GAWindowEligibility expected) {
    assert(ga_ax_window_eligibility(42, 7, bounds) == expected);
    tests++;
}
int main(int argc, char **argv) {
    reset();
    if (argc == 2 && strcmp(argv[1], "missing-symbol") == 0) {
        missing_symbol = true;
        expect(GAWindowIdentityUnavailable);
        puts("missing AX window-ID symbol fails closed: passed");
        return 0;
    }
    // CG layer is deliberately not an authorization input. A focused floating
    // window uses the same exact-ID contract as an ordinary layer-zero window.
    expect(GAWindowFocused);
    missing_windows = true;
    expect(GAWindowFocused); // Focused panels need not appear in AXWindows.
    reset();
    focused = &overlay;
    expect(GAWindowUnfocused); // Same bounds, different actual window ID.
    assert(!ga_ax_window_matches(focused, 42, 7, bounds));
    reset();
    windows.values[1] = &overlay;
    focused = &overlay;
    expect(GAWindowIdentityUnavailable); // CG-only overlay cannot be an AX target.
    reset();
    target.pid = 43;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.id = 9;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.id_error = true;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.pid_error = true;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.role = "AXGroup";
    expect(GAWindowIdentityUnavailable);
    reset();
    target.position.x++;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.size.x++;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.missing_position = true;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.position.type = OtherType;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.size.kind = kAXValueCGPointType;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.timeout_error = true;
    expect(GAWindowIdentityUnavailable);
    reset();
    app.timeout_error = true;
    expect(GAWindowIdentityUnavailable);
    reset();
    target.type = OtherType;
    expect(GAWindowIdentityUnavailable);
    reset();
    missing_app = true;
    expect(GAWindowIdentityUnavailable);
    reset();
    missing_focus = true;
    expect(GAWindowUnfocused); // Capture-only, never input/discovery eligible.
    missing_windows = true;
    expect(GAWindowIdentityUnavailable);
    reset();
    focused = NULL;
    windows.type = OtherType;
    expect(GAWindowIdentityUnavailable);
    reset();
    assert(!ga_ax_window_matches(NULL, 42, 7, bounds));
    assert(!ga_ax_window_matches(&target, 0, 7, bounds));
    assert(!ga_ax_window_matches(&target, 42, 0, bounds));
    assert(!ga_ax_window_matches(&target, 42, 7, (CGRect){{NAN, 20}, {300, 200}}));
    assert(!ga_ax_window_matches(&target, 42, 7, (CGRect){{10, 20}, {0, 200}}));
    assert(!ga_ax_window_matches(&target, 42, 7, (CGRect){{10, 20}, {300, INFINITY}}));
    printf("AX/CG window identity: %u eligibility cases and boundary assertions passed\n", tests);
    return 0;
}
