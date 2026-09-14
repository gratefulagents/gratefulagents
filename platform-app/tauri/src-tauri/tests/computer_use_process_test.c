#include <assert.h>
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <string.h>
#include <libproc.h>
#include "../src/computer_use_process.h"

static struct proc_bsdinfo response;
static int response_size;
static int query_count;

int proc_pidinfo(int pid, int flavor, uint64_t arg, void *buffer, int buffersize) {
    assert(pid == 42);
    assert(flavor == PROC_PIDTBSDINFO);
    assert(arg == 0);
    assert(buffersize == sizeof(response));
    query_count++;
    memcpy(buffer, &response, sizeof(response));
    return response_size;
}

static void expect_rejected(void) {
    GAProcessIdentity identity = {42, 123, 456};
    assert(!ga_process_identity_read(42, &identity));
    assert(identity.pid == 0 && identity.start_seconds == 0 && identity.start_microseconds == 0);
}

int main(void) {
    GAProcessIdentity identity;
    assert(!ga_process_identity_read(0, &identity));
    assert(!ga_process_identity_read(-1, &identity));
    assert(!ga_process_identity_read(INT_MIN, &identity));
    assert(!ga_process_identity_read(42, NULL));
    assert(query_count == 0);

    response = (struct proc_bsdinfo){
        .pbi_pid = 42, .pbi_start_tvsec = 1700000000, .pbi_start_tvusec = 123456,
    };
    response_size = sizeof(response);
    errno = ESRCH; // Stale errno must not reject a complete successful response.
    assert(ga_process_identity_read(42, &identity));
    assert(identity.pid == 42 && identity.start_seconds == 1700000000 && identity.start_microseconds == 123456);
    GAProcessIdentity again;
    assert(ga_process_identity_read(42, &again));
    assert(ga_process_identity_equal(identity, again));

    response_size = 0;
    errno = ESRCH;
    expect_rejected();
    response_size = -1;
    errno = EPERM;
    expect_rejected();
    response_size = sizeof(response) - 1;
    expect_rejected();
    response_size = sizeof(response) + 1;
    expect_rejected();
    response_size = sizeof(response);
    response.pbi_pid = 43;
    expect_rejected();
    response.pbi_pid = 42;
    response.pbi_flags = PROC_FLAG_INEXIT;
    expect_rejected();
    response.pbi_flags = 0;
    response.pbi_start_tvsec = 0;
    expect_rejected();
    response.pbi_start_tvsec = 1700000000;
    response.pbi_start_tvusec = 1000000;
    expect_rejected();

    response.pbi_start_tvusec = 123457;
    assert(ga_process_identity_read(42, &again));
    assert(!ga_process_identity_equal(identity, again));
    response.pbi_start_tvusec = 123456;
    response.pbi_start_tvsec++;
    assert(ga_process_identity_read(42, &again));
    assert(!ga_process_identity_equal(identity, again));
    again = identity;
    again.pid++;
    assert(!ga_process_identity_equal(identity, again));

    response.pbi_start_tvsec = UINT64_C(9007199254740992);
    response.pbi_start_tvusec = 0;
    assert(ga_process_identity_read(42, &identity));
    response.pbi_start_tvsec++;
    assert(ga_process_identity_read(42, &again));
    assert(!ga_process_identity_equal(identity, again));
    response.pbi_start_tvusec = 999999;
    assert(ga_process_identity_read(42, &identity));
    puts("PASS: kernel identity validation, lookup failures, PID reuse, exact integer comparison");
    return 0;
}
