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
GAWindowEligibility ga_ax_window_eligibility(pid_t pid, CGWindowID window_id, CGRect bounds);

#endif
