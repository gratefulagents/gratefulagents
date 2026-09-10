#import <AppKit/AppKit.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <ImageIO/ImageIO.h>

#include <stdint.h>
#include <unistd.h>
#include <stdlib.h>
#include <string.h>

typedef void (*GAReply)(void *, const char *);

static void replyJSON(GAReply reply, void *context, id value) {
    NSData *data = [NSJSONSerialization dataWithJSONObject:value options:NSJSONWritingFragmentsAllowed error:nil];
    NSString *json = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
    reply(context, json.UTF8String);
}

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
        replyJSON(reply, _context, value);
    }
}
@end

API_AVAILABLE(macos(15.2))
@interface GAWindowShare : NSObject <SCContentSharingPickerObserver, SCStreamDelegate>
@property uint64_t token;
@property BOOL valid;
@property (strong) SCContentFilter *filter;
@property (strong) SCWindow *window;
@property (strong) NSRunningApplication *application;
@property (strong) SCStream *stream;
@property (strong) GACompletion *selection;
- (void)invalidate;
- (NSDictionary *)snapshot;
@end

static GAWindowShare *current API_AVAILABLE(macos(15.2));
static NSDictionary<NSNumber *, GAWindowShare *> *agentWindows API_AVAILABLE(macos(15.2));
static uint64_t agentInventoryRevision;

@implementation GAWindowShare
- (void)invalidate {
    _valid = NO;
    _filter = nil;
    _window = nil;
    _application = nil;
    [_selection finish:NSNull.null];
    _selection = nil;
    dispatch_async(dispatch_get_main_queue(), ^{
        @synchronized(GAWindowShare.class) {
            [SCContentSharingPicker.sharedPicker removeObserver:self];
            if (current == self) SCContentSharingPicker.sharedPicker.active = NO;
            [self.stream stopCaptureWithCompletionHandler:nil];
            self.stream = nil;
        }
    });
}

- (NSDictionary *)snapshot {
    if (!_valid || !_filter || !_window || _application.terminated) return nil;
    NSRunningApplication *live = [NSRunningApplication runningApplicationWithProcessIdentifier:_application.processIdentifier];
    if (!live || !live.launchDate || ![live.launchDate isEqual:_application.launchDate] ||
        ![live.bundleIdentifier isEqual:_window.owningApplication.bundleIdentifier]) return nil;
    // CG metadata (IDs, PIDs, bounds, order) needs no Screen Recording grant.
    // Never use window titles or images from this list to authorize a target.
    NSArray *windows = CFBridgingRelease(CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID));
    NSNumber *first = nil;
    NSDictionary *selected = nil;
    for (NSDictionary *info in windows) {
        if ([info[(__bridge NSString *)kCGWindowOwnerPID] intValue] != _application.processIdentifier) continue;
        if ([info[(__bridge NSString *)kCGWindowLayer] intValue] != 0) continue;
        NSNumber *wid = info[(__bridge NSString *)kCGWindowNumber];
        if (!first) first = wid;
        if (wid.unsignedIntValue == _window.windowID) selected = info;
    }
    CGRect bounds;
    if (!selected || !CGRectMakeWithDictionaryRepresentation((__bridge CFDictionaryRef)selected[(__bridge NSString *)kCGWindowBounds], &bounds) ||
        CGRectIsEmpty(bounds) || CGRectIsInfinite(bounds) || CGRectIsNull(bounds)) return nil;
    pid_t front = NSWorkspace.sharedWorkspace.frontmostApplication.processIdentifier;
    return @{
        @"windowId": @(_window.windowID), @"processId": @(_application.processIdentifier),
        @"application": _window.owningApplication.applicationName,
        @"title": _window.title ?: @"", @"frontmost": @(first.unsignedIntValue == _window.windowID),
        @"focusAllowed": @(front == _application.processIdentifier || front == getpid()),
        @"geometry": @{@"x": @((int32_t)bounds.origin.x), @"y": @((int32_t)bounds.origin.y),
                        @"width": @((uint32_t)bounds.size.width), @"height": @((uint32_t)bounds.size.height)}
    };
}

- (void)contentSharingPicker:(SCContentSharingPicker *)picker didUpdateWithFilter:(SCContentFilter *)filter forStream:(SCStream *)stream {
    @synchronized(GAWindowShare.class) {
        if (current != self || !_valid) return;
        // A changed filter never reauthorizes a running session, even for the same ID.
        if (_filter || stream || filter.style != SCShareableContentStyleWindow || filter.includedWindows.count != 1) {
            [self invalidate];
            return;
        }
        SCWindow *window = filter.includedWindows.firstObject;
        SCRunningApplication *owner = window.owningApplication;
        NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:owner.processID];
        if (!owner || owner.processID == getpid() || !owner.applicationName.length || !owner.bundleIdentifier.length ||
            !app.launchDate || ![app.bundleIdentifier isEqual:owner.bundleIdentifier] ||
            [owner.bundleIdentifier isEqual:NSBundle.mainBundle.bundleIdentifier]) {
            [_selection finish:@{@"error": @"The supervisor or unidentified application cannot be shared"}];
            [self invalidate];
            return;
        }
        _filter = filter;
        _window = window;
        _application = app;
        NSDictionary *snapshot = [self snapshot];
        if (!snapshot) {
            [_selection finish:@{@"error": @"The selected window is unavailable"}];
            [self invalidate];
            return;
        }
        [_selection finish:snapshot];
        _selection = nil;
    }
}
- (void)contentSharingPicker:(SCContentSharingPicker *)picker didCancelForStream:(SCStream *)stream {
    @synchronized(GAWindowShare.class) { [self invalidate]; }
}
- (void)contentSharingPickerStartDidFailWithError:(NSError *)error {
    @synchronized(GAWindowShare.class) {
        NSString *message = [NSString stringWithFormat:@"macOS could not open the window sharing picker (%@, code %ld). Quit and reopen gratefulagents, then try again.", error.domain, (long)error.code];
        [_selection finish:@{@"error": message}];
        [self invalidate];
    }
}
- (void)streamDidBecomeInactive:(SCStream *)stream {
    @synchronized(GAWindowShare.class) { [self invalidate]; }
}
- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    @synchronized(GAWindowShare.class) { [self invalidate]; }
}
@end

bool ga_window_sharing_supported(void) {
    if (@available(macOS 15.2, *)) return true;
    return false;
}

void ga_window_sharing_revoke(uint64_t token) {
    if (@available(macOS 15.2, *)) {
        @synchronized(GAWindowShare.class) {
            if (token == 0 || current.token == token) [current invalidate];
            if (token == 0) { agentWindows = nil; agentInventoryRevision++; }
        }
    }
}

void ga_window_sharing_pick(uint64_t token, GAReply reply, void *context) {
    if (@available(macOS 15.2, *)) {
        GAWindowShare *share = [GAWindowShare new];
        share.token = token;
        share.valid = YES;
        share.selection = [GACompletion new];
        share.selection.reply = reply;
        share.selection.context = context;
        @synchronized(GAWindowShare.class) {
            [current invalidate];
            current = share;
        }
        dispatch_async(dispatch_get_main_queue(), ^{
            @synchronized(GAWindowShare.class) {
                if (current != share || !share.valid) return;
                SCContentSharingPicker *picker = SCContentSharingPicker.sharedPicker;
                SCContentSharingPickerConfiguration *config = [SCContentSharingPickerConfiguration new];
                config.allowedPickerModes = SCContentSharingPickerModeSingleWindow;
                config.allowsChangingSelectedContent = NO;
                if (NSBundle.mainBundle.bundleIdentifier) config.excludedBundleIDs = @[NSBundle.mainBundle.bundleIdentifier];
                NSMutableArray *ids = [NSMutableArray new];
                for (NSWindow *window in NSApp.windows) {
                    if (window.windowNumber > 0) [ids addObject:@(window.windowNumber)];
                }
                config.excludedWindowIDs = ids;
                picker.defaultConfiguration = config;
                picker.maximumStreamCount = @1;
                [picker addObserver:share];
                picker.active = YES;
                [picker presentPickerUsingContentStyle:SCShareableContentStyleWindow];
            }
        });
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 60 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
            @synchronized(GAWindowShare.class) {
                if (share.selection) {
                    // A missing OS callback is not a user cancellation. Complete
                    // with an error before invalidation clears the pending reply.
                    [share.selection finish:@{@"error": @"The macOS window sharing picker timed out after 60 seconds without a selection. If no picker appeared, quit and reopen gratefulagents, then try again. Agent chooses windows uses a separate Screen Recording permission and does not need this picker."}];
                    [share invalidate];
                }
            }
        });
    } else {
        replyJSON(reply, context, @{@"error": @"Computer use requires macOS 15.2 or later"});
    }
}

char *ga_window_sharing_snapshot(uint64_t token) {
    @autoreleasepool {
        if (@available(macOS 15.2, *)) {
            @synchronized(GAWindowShare.class) {
                NSDictionary *snapshot = current.token == token ? [current snapshot] : nil;
                if (!snapshot) return NULL;
                NSData *data = [NSJSONSerialization dataWithJSONObject:snapshot options:0 error:nil];
                return strdup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding].UTF8String);
            }
        }
        return NULL;
    }
}

void ga_window_sharing_free(char *value) { free(value); }

void ga_window_sharing_capture(uint64_t token, uint32_t width, uint32_t height, GAReply reply, void *context) {
    GACompletion *completion = [GACompletion new];
    completion.reply = reply;
    completion.context = context;
    if (@available(macOS 15.2, *)) {
        dispatch_async(dispatch_get_main_queue(), ^{
            @synchronized(GAWindowShare.class) {
                GAWindowShare *share = current;
                if (share.token != token || ![share snapshot] || !width || !height || width > 1920 || height > 1080) {
                    [completion finish:@{@"error": @"Window sharing is unavailable or revoked"}];
                    return;
                }
                SCContentFilter *filter = share.filter;
                SCStreamConfiguration *config = [SCStreamConfiguration new];
                config.width = width;
                config.height = height;
                config.showsCursor = NO;
                config.capturesAudio = NO;
                config.ignoreShadowsSingleWindow = YES;
                config.includeChildWindows = NO;
                config.scalesToFit = YES;
                void (^capture)(void) = ^{
                    @synchronized(GAWindowShare.class) {
                        if (!share.valid || current != share) {
                            [completion finish:@{@"error": @"Window sharing was revoked"}];
                            return;
                        }
                        [SCScreenshotManager captureImageWithFilter:filter configuration:config completionHandler:^(CGImageRef image, NSError *error) {
                            @synchronized(GAWindowShare.class) {
                                if (error || !image || !share.valid || current != share || ![share snapshot] ||
                                    CGImageGetWidth(image) != width || CGImageGetHeight(image) != height) {
                                    [share invalidate];
                                    [completion finish:@{@"error": @"Window capture failed or sharing was revoked; select the window again"}];
                                    return;
                                }
                                NSMutableData *png = [NSMutableData new];
                                CGImageDestinationRef destination = CGImageDestinationCreateWithData((__bridge CFMutableDataRef)png, CFSTR("public.png"), 1, NULL);
                                BOOL encoded = NO;
                                if (destination) {
                                    CGImageDestinationAddImage(destination, image, NULL);
                                    encoded = CGImageDestinationFinalize(destination);
                                    CFRelease(destination);
                                }
                                [completion finish:encoded ? @{@"dataUrl": [@"data:image/png;base64," stringByAppendingString:[png base64EncodedStringWithOptions:0]]} : @{@"error": @"Cannot encode window capture"}];
                            }
                        }];
                    }
                };
                if (share.stream) {
                    capture();
                } else {
                    // Keep an OS sharing session solely to receive user/system stop notifications.
                    // Screenshots always use the original picker filter, never a reconstructed one.
                    SCStreamConfiguration *monitor = [SCStreamConfiguration new];
                    monitor.width = 2;
                    monitor.height = 2;
                    monitor.minimumFrameInterval = CMTimeMake(1, 1);
                    monitor.showsCursor = NO;
                    monitor.capturesAudio = NO;
                    monitor.includeChildWindows = NO;
                    share.stream = [[SCStream alloc] initWithFilter:filter configuration:monitor delegate:share];
                    [share.stream startCaptureWithCompletionHandler:^(NSError *error) {
                        if (error) {
                            @synchronized(GAWindowShare.class) { [share invalidate]; }
                            [completion finish:@{@"error": @"macOS could not start window sharing"}];
                        } else capture();
                    }];
                }
                dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 8 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
                    [completion finish:@{@"error": @"Window capture timed out"}];
                });
            }
        });
    } else {
        [completion finish:@{@"error": @"Computer use requires macOS 15.2 or later"}];
    }
}

void ga_agent_windows(GAReply reply, void *context) {
    GACompletion *completion = [GACompletion new];
    completion.reply = reply;
    completion.context = context;
    if (@available(macOS 15.2, *)) {
        if (!CGPreflightScreenCaptureAccess()) {
            [completion finish:@{@"error": @"Agent-choice Screen Recording permission is required"}];
            return;
        }
        __block uint64_t revision;
        @synchronized(GAWindowShare.class) { revision = ++agentInventoryRevision; }
        [SCShareableContent getShareableContentExcludingDesktopWindows:YES onScreenWindowsOnly:YES completionHandler:^(SCShareableContent *content, NSError *error) {
            @synchronized(GAWindowShare.class) {
                if (error || !content || revision != agentInventoryRevision || !CGPreflightScreenCaptureAccess()) {
                    [completion finish:@{@"error": @"Window discovery failed or was revoked"}];
                    return;
                }
                NSMutableDictionary *inventory = [NSMutableDictionary new];
                NSMutableArray *metadata = [NSMutableArray new];
                for (SCWindow *window in content.windows) {
                    SCRunningApplication *owner = window.owningApplication;
                    NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:owner.processID];
                    if (!owner || owner.processID == getpid() || !owner.applicationName.length || !owner.bundleIdentifier.length ||
                        !app.launchDate || ![app.bundleIdentifier isEqual:owner.bundleIdentifier] ||
                        [owner.bundleIdentifier isEqual:NSBundle.mainBundle.bundleIdentifier]) continue;
                    GAWindowShare *share = [GAWindowShare new];
                    share.valid = YES;
                    share.window = window;
                    share.application = app;
                    share.filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:window];
                    NSDictionary *snapshot = [share snapshot];
                    if (!snapshot || ![snapshot[@"frontmost"] boolValue]) continue;
                    inventory[@(window.windowID)] = share;
                    [metadata addObject:snapshot];
                    if (metadata.count == 64) break;
                }
                agentWindows = inventory;
                [completion finish:metadata];
            }
        }];
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 8 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
            @synchronized(GAWindowShare.class) {
                if (revision == agentInventoryRevision) agentInventoryRevision++;
                [completion finish:@{@"error": @"Window discovery timed out"}];
            }
        });
    } else [completion finish:@{@"error": @"Computer use requires macOS 15.2 or later"}];
}

char *ga_agent_window_snapshot(uint32_t window_id) {
    @autoreleasepool {
        if (@available(macOS 15.2, *)) {
            @synchronized(GAWindowShare.class) {
                NSDictionary *snapshot = CGPreflightScreenCaptureAccess() ? [agentWindows[@(window_id)] snapshot] : nil;
                if (!snapshot) return NULL;
                NSData *data = [NSJSONSerialization dataWithJSONObject:snapshot options:0 error:nil];
                return strdup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding].UTF8String);
            }
        }
        return NULL;
    }
}

bool ga_agent_window_select(uint64_t token, uint32_t window_id) {
    if (@available(macOS 15.2, *)) {
        @synchronized(GAWindowShare.class) {
            GAWindowShare *share = agentWindows[@(window_id)];
            if (!CGPreflightScreenCaptureAccess() || ![share snapshot]) return false;
            [current invalidate];
            share.token = token;
            current = share;
            return true;
        }
    }
    return false;
}
