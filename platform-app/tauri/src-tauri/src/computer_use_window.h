#ifndef GA_COMPUTER_USE_WINDOW_H
#define GA_COMPUTER_USE_WINDOW_H

#include <ApplicationServices/ApplicationServices.h>
#include <stdbool.h>

typedef enum {
    GAWindowIdentityUnavailable,
    GAWindowUnfocused,
    GAWindowFocused
} GAWindowEligibility;

bool ga_ax_window_matches(AXUIElementRef window, pid_t pid, CGWindowID window_id, CGRect bounds);
AXUIElementRef ga_ax_window_copy(pid_t pid, CGWindowID window_id, CGRect bounds, AXUIElementRef retained, GAWindowEligibility *eligibility);

#endif
