//go:build cgo

#import <AppKit/AppKit.h>
#import <UserNotifications/UserNotifications.h>

#include "present_darwin.h"
#include "_cgo_export.h"

static NSString* const kCategory = @"amarit.update";
static NSString* const kRelaunch = @"amarit.relaunch";

// UNUserNotificationCenter throws in a process with no bundle, such as a
// test binary or a command-line program, so amarit leaves notifications to
// processes that have a bundle identifier.
static BOOL HasBundle(void) {
  return NSBundle.mainBundle.bundleIdentifier.length > 0;
}

static void OnMain(dispatch_block_t block) {
  if (NSThread.isMainThread) {
    block();
  } else {
    dispatch_async(dispatch_get_main_queue(), block);
  }
}

// The app's icon with a progress bar along its foot.
@interface AmaritDockView : NSView
@property double fraction;
@end

@implementation AmaritDockView
- (void)drawRect:(NSRect)dirty {
  NSRect b = self.bounds;
  [NSApp.applicationIconImage drawInRect:b];
  NSRect bar = NSMakeRect(NSWidth(b) * 0.12, NSHeight(b) * 0.1, NSWidth(b) * 0.76, NSHeight(b) * 0.09);
  CGFloat r = NSHeight(bar) / 2;
  [[NSColor colorWithWhite:0 alpha:0.55] setFill];
  [[NSBezierPath bezierPathWithRoundedRect:bar xRadius:r yRadius:r] fill];
  NSRect fill = NSInsetRect(bar, 1.5, 1.5);
  fill.size.width = MAX(fill.size.height, fill.size.width * MIN(MAX(self.fraction, 0), 1));
  CGFloat fr = NSHeight(fill) / 2;
  [NSColor.controlAccentColor setFill];
  [[NSBezierPath bezierPathWithRoundedRect:fill xRadius:fr yRadius:fr] fill];
}
@end

void macapp_dock_progress(double fraction) {
  OnMain(^{
    NSDockTile* tile = NSApp.dockTile;
    AmaritDockView* view = [tile.contentView isKindOfClass:AmaritDockView.class] ? (AmaritDockView*)tile.contentView : nil;
    if (view == nil) {
      view = [[AmaritDockView alloc] initWithFrame:NSMakeRect(0, 0, tile.size.width, tile.size.height)];
      tile.contentView = view;
    }
    view.fraction = fraction;
    [tile display];
  });
}

void macapp_dock_clear(void) {
  OnMain(^{
    NSDockTile* tile = NSApp.dockTile;
    if ([tile.contentView isKindOfClass:AmaritDockView.class]) {
      tile.contentView = nil;
      [tile display];
    }
  });
}

// Installed only when the app has no delegate of its own; an app that has
// one forwards amarit's category to macapp.HandleNotificationResponse.
@interface AmaritNotificationDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation AmaritNotificationDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter*)center
       willPresentNotification:(UNNotification*)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler {
  completionHandler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList);
}

- (void)userNotificationCenter:(UNUserNotificationCenter*)center
    didReceiveNotificationResponse:(UNNotificationResponse*)response
             withCompletionHandler:(void (^)(void))completionHandler {
  macappNotificationResponse((char*)response.notification.request.content.categoryIdentifier.UTF8String,
                             (char*)response.actionIdentifier.UTF8String);
  completionHandler();
}
@end

void macapp_notify(const char* ctitle, const char* cbody, const char* cbutton) {
  if (!HasBundle()) return;
  NSString* title = [NSString stringWithUTF8String:ctitle];
  NSString* body = [NSString stringWithUTF8String:cbody];
  NSString* button = [NSString stringWithUTF8String:cbutton];
  OnMain(^{
    UNUserNotificationCenter* center = UNUserNotificationCenter.currentNotificationCenter;
    static AmaritNotificationDelegate* ours;
    if (center.delegate == nil) {
      ours = [[AmaritNotificationDelegate alloc] init];
      center.delegate = ours;
    }
    UNNotificationAction* relaunch = [UNNotificationAction actionWithIdentifier:kRelaunch
                                                                          title:button
                                                                        options:UNNotificationActionOptionNone];
    UNNotificationCategory* category = [UNNotificationCategory categoryWithIdentifier:kCategory
                                                                              actions:@[ relaunch ]
                                                                    intentIdentifiers:@[]
                                                                              options:0];
    // Categories are set as a whole: the app's own are kept beside ours.
    [center getNotificationCategoriesWithCompletionHandler:^(NSSet<UNNotificationCategory*>* existing) {
      NSMutableSet* all = [NSMutableSet set];
      for (UNNotificationCategory* c in existing) {
        if (![c.identifier isEqualToString:kCategory]) [all addObject:c];
      }
      [all addObject:category];
      [center setNotificationCategories:all];
      [center requestAuthorizationWithOptions:UNAuthorizationOptionAlert
                            completionHandler:^(BOOL granted, NSError* error) {
                              if (!granted) return;
                              UNMutableNotificationContent* content = [[UNMutableNotificationContent alloc] init];
                              content.title = title;
                              content.body = body;
                              content.categoryIdentifier = kCategory;
                              UNNotificationRequest* request = [UNNotificationRequest requestWithIdentifier:kCategory
                                                                                                    content:content
                                                                                                    trigger:nil];
                              [center addNotificationRequest:request withCompletionHandler:nil];
                            }];
    }];
  });
}

void macapp_notify_remove(void) {
  if (!HasBundle()) return;
  OnMain(^{
    UNUserNotificationCenter* center = UNUserNotificationCenter.currentNotificationCenter;
    [center removeDeliveredNotificationsWithIdentifiers:@[ kCategory ]];
    [center removePendingNotificationRequestsWithIdentifiers:@[ kCategory ]];
  });
}

char* macapp_app_name(void) {
  NSString* name = [NSBundle.mainBundle objectForInfoDictionaryKey:@"CFBundleDisplayName"];
  if (name.length == 0) name = [NSBundle.mainBundle objectForInfoDictionaryKey:@"CFBundleName"];
  if (name.length == 0) name = NSProcessInfo.processInfo.processName;
  return strdup(name.UTF8String);
}
