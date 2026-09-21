#import <AppKit/AppKit.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <ImageIO/ImageIO.h>
#include <stdint.h>

typedef void (*GAReply)(void *, const char *);
@interface GACompletion : NSObject
@property GAReply reply;
@property void *context;
- (void)finish:(id)value;
@end
@implementation GACompletion
- (void)finish:(id)value {
    @synchronized(self) {
        if (!_reply) return;
        GAReply reply = _reply;
        _reply = NULL;
        NSData *data = [NSJSONSerialization dataWithJSONObject:value options:NSJSONWritingFragmentsAllowed error:nil];
        reply(_context, [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding].UTF8String);
    }
}
@end
static uint64_t selectedToken;
static CGDirectDisplayID selectedDisplay;
static NSString *selectedUUID;
static NSAlert *picker;
static GACompletion *picking;
static GACompletion *capturing;

bool ga_display_supported(void) { if (@available(macOS 15.2, *)) return true; return false; }
static NSString *displayUUID(CGDirectDisplayID display) {
    CFUUIDRef uuid = CGDisplayCreateUUIDFromDisplayID(display);
    if (!uuid) return nil;
    NSString *value = CFBridgingRelease(CFUUIDCreateString(NULL, uuid));
    CFRelease(uuid);
    return value;
}
bool ga_display_valid(uint64_t token, uint32_t display) {
    @synchronized(GACompletion.class) {
        return token && token == selectedToken && display == selectedDisplay && CGDisplayIsActive(display)
            && [selectedUUID isEqual:displayUUID(display)] && CGPreflightScreenCaptureAccess();
    }
}
void ga_display_revoke(uint64_t token) {
    @synchronized(GACompletion.class) {
        if (token && token != selectedToken) return;
        selectedToken = 0;
        selectedDisplay = 0;
        selectedUUID = nil;
        [picking finish:NSNull.null]; picking = nil;
        [capturing finish:@{@"error": @"Display sharing revoked"}]; capturing = nil;
        NSAlert *old = picker; picker = nil;
        dispatch_async(dispatch_get_main_queue(), ^{ if (old) { if (NSApp.modalWindow == old.window) [NSApp abortModal]; [old.window orderOut:nil]; } });
    }
}
void ga_display_pick(uint64_t token, GAReply reply, void *context) {
    GACompletion *completion = [GACompletion new]; completion.reply = reply; completion.context = context;
    @synchronized(GACompletion.class) { selectedToken = token; picking = completion; }
    dispatch_async(dispatch_get_main_queue(), ^{
        @synchronized(GACompletion.class) { if (token != selectedToken) { [completion finish:NSNull.null]; return; } }
        NSArray<NSScreen *> *screens = NSScreen.screens;
        if (!CGPreflightScreenCaptureAccess() || !screens.count) { [completion finish:@{@"error": @"Screen Recording permission and an active display are required"}]; return; }
        NSAlert *alert = [NSAlert new];
        alert.messageText = @"Select a display for desktop control";
        alert.informativeText = @"All visible content on this display may be shared, including sensitive apps. Pointer input stays on this display. Keyboard input follows OS focus and can affect other displays. This is not window isolation.";
        [alert addButtonWithTitle:@"Select display"]; [alert addButtonWithTitle:@"Cancel"];
        NSPopUpButton *choices = [[NSPopUpButton alloc] initWithFrame:NSMakeRect(0, 0, 380, 28) pullsDown:NO];
        for (NSScreen *screen in screens) [choices addItemWithTitle:[NSString stringWithFormat:@"%@ (display %@)", screen.localizedName, screen.deviceDescription[@"NSScreenNumber"]]];
        alert.accessoryView = choices;
        @synchronized(GACompletion.class) { if (token != selectedToken) { [completion finish:NSNull.null]; return; } picker = alert; }
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 120 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
            @synchronized(GACompletion.class) { if (picker == alert) ga_display_revoke(token); }
        });
        NSModalResponse response = [alert runModal];
        [alert.window orderOut:nil];
        @synchronized(GACompletion.class) {
            if (picker == alert) picker = nil;
            if (token != selectedToken || response != NSAlertFirstButtonReturn) { [completion finish:NSNull.null]; return; }
            NSScreen *screen = screens[choices.indexOfSelectedItem];
            selectedDisplay = [screen.deviceDescription[@"NSScreenNumber"] unsignedIntValue];
            selectedUUID = displayUUID(selectedDisplay);
            [completion finish:@{@"displayId": @(selectedDisplay), @"name": screen.localizedName}];
            picking = nil;
        }
    });
}
void ga_display_capture(uint64_t token, uint32_t display, uint32_t width, uint32_t height, GAReply reply, void *context) {
    GACompletion *completion = [GACompletion new]; completion.reply = reply; completion.context = context;
    if (!ga_display_valid(token, display)) { [completion finish:@{@"error": @"Selected display is unavailable; reconnect"}]; return; }
    @synchronized(GACompletion.class) { capturing = completion; }
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 8 * NSEC_PER_SEC), dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0), ^{ [completion finish:@{@"error": @"Display capture timed out"}]; });
    if (@available(macOS 15.2, *)) {
        [SCShareableContent getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:YES completionHandler:^(SCShareableContent *content, NSError *error) {
            if (!ga_display_valid(token, display)) { [completion finish:@{@"error": @"Display sharing revoked"}]; return; }
            SCDisplay *selected = nil;
            for (SCDisplay *candidate in content.displays) if (candidate.displayID == display) selected = candidate;
            if (!selected || error) { [completion finish:@{@"error": error.localizedDescription ?: @"Selected display disconnected"}]; return; }
            SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:selected excludingWindows:@[]];
            SCStreamConfiguration *configuration = [SCStreamConfiguration new];
            configuration.width = width; configuration.height = height; configuration.showsCursor = NO;
            [SCScreenshotManager captureImageWithFilter:filter configuration:configuration completionHandler:^(CGImageRef image, NSError *captureError) {
                if (!ga_display_valid(token, display)) { [completion finish:@{@"error": @"Display sharing revoked"}]; return; }
                if (!image || captureError) { [completion finish:@{@"error": captureError.localizedDescription ?: @"Display capture unavailable"}]; return; }
                if (CGImageGetWidth(image) != width || CGImageGetHeight(image) != height) { [completion finish:@{@"error": @"Display capture dimensions changed"}]; return; }
                NSMutableData *data = [NSMutableData new];
                CGImageDestinationRef destination = CGImageDestinationCreateWithData((__bridge CFMutableDataRef)data, CFSTR("public.png"), 1, NULL);
                if (!destination) { [completion finish:@{@"error": @"Cannot encode display capture"}]; return; }
                CGImageDestinationAddImage(destination, image, NULL);
                bool encoded = CGImageDestinationFinalize(destination); CFRelease(destination);
                if (!encoded || data.length > 8 * 1024 * 1024) { [completion finish:@{@"error": @"Display capture exceeds bounded PNG size"}]; return; }
                [completion finish:@{@"dataUrl": [@"data:image/png;base64," stringByAppendingString:[data base64EncodedStringWithOptions:0]]}];
                @synchronized(GACompletion.class) { if (capturing == completion) capturing = nil; }
            }];
        }];
    } else [completion finish:@{@"error": @"Desktop capture requires macOS 15.2 or later"}];
}

void ga_desktop_prepare_keyboard(void) {
    dispatch_sync(dispatch_get_main_queue(), ^{ if (NSApp.active) [NSApp hide:nil]; });
}
