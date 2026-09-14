#ifndef GA_COMPUTER_USE_PROCESS_H
#define GA_COMPUTER_USE_PROCESS_H

#include <stdbool.h>
#include <stdint.h>

typedef struct {
    uint64_t pid;
    uint64_t start_seconds;
    uint64_t start_microseconds;
} GAProcessIdentity;

bool ga_process_identity_read(int32_t pid, GAProcessIdentity *identity);

static inline bool ga_process_identity_equal(GAProcessIdentity a, GAProcessIdentity b) {
    return a.pid == b.pid && a.start_seconds == b.start_seconds &&
        a.start_microseconds == b.start_microseconds;
}

#endif
